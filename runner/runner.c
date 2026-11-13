#define _GNU_SOURCE

#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <poll.h>
#include <sched.h>
#include <signal.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define PROTOCOL 1
#define MAX_ENV 256
#define OUTPUT_MODE 0644
#define NO_FD (-1)
#define NO_EXIT_CODE (-1)

#define EXIT_USAGE 2
#define EXIT_EXEC_FAILED 127

#define PIPE_READ 0
#define PIPE_WRITE 1

#define NSEC_PER_SEC 1000000000LL
#define NSEC_PER_MSEC 1000000LL
#define USEC_PER_SEC 1000000LL
#define SEC_PER_DAY (24 * 60 * 60)
#define MAX_TIMEOUT_SEC (30.0 * SEC_PER_DAY)

typedef struct
{
	char *env[MAX_ENV];
	int env_count;
	double timeout_sec;
	bool pin_cpus;
	cpu_set_t cpus;
	const char *stdout_path;
	const char *stderr_path;
	const char *result_path;
	char **command;
} Options;

typedef struct
{
	int exit_code;
	int term_signal;
	bool timed_out;
	int exec_errno;
	long long wall_ns;
	struct rusage resources;
} Result;

static void open_standard_fds(void)
{
	int fd;

	for (fd = STDIN_FILENO; fd <= STDERR_FILENO; fd++) {
		if (fcntl(fd, F_GETFD) >= 0 || errno != EBADF)
			continue;
		if (open("/dev/null", O_RDWR) != fd)
			exit(EXIT_FAILURE);
	}
}

static void usage(const char *message)
{
	fprintf(stderr, "runner: %s\n", message);
	fprintf(stderr,
		"usage: runner [--env K=V]... [--timeout SEC] [--cpus LIST] [--stdout FILE] [--stderr FILE] [--result FILE] -- CMD [ARG]...\n");
	exit(EXIT_USAGE);
}

static void fail(const char *what)
{
	fprintf(stderr, "runner: %s: %s\n", what, strerror(errno));
	exit(EXIT_FAILURE);
}

static char *option_value(int argc, char **argv, int *arg_index)
{
	if (*arg_index + 1 >= argc)
		usage("option needs a value");
	return argv[++*arg_index];
}

static void add_env(Options *options, char *pair)
{
	if (pair[0] == '=' || !strchr(pair, '='))
		usage("--env needs KEY=VALUE");
	if (options->env_count == MAX_ENV)
		usage("too many --env options");
	options->env[options->env_count++] = pair;
}

static double parse_seconds(const char *text)
{
	char *end;
	double seconds;

	errno = 0;
	seconds = strtod(text, &end);
	if (errno || end == text || *end || !(seconds >= 0 && seconds <= MAX_TIMEOUT_SEC))
		usage("--timeout needs seconds from 0 to 30 days");
	return seconds;
}

static size_t parse_cpu(const char *text, char **end)
{
	long cpu;

	if (*text < '0' || *text > '9')
		usage("--cpus needs a list like 0-3,6");
	errno = 0;
	cpu = strtol(text, end, 10);
	if (errno || cpu >= CPU_SETSIZE)
		usage("--cpus has a CPU number above the limit");
	return (size_t)cpu;
}

static void parse_cpus(const char *text, Options *options)
{
	const char *cursor = text;

	CPU_ZERO(&options->cpus);
	options->pin_cpus = true;
	for (;;) {
		char *end;
		size_t first = parse_cpu(cursor, &end);
		size_t last = first;
		size_t cpu;

		if (*end == '-') {
			last = parse_cpu(end + 1, &end);
			if (last < first)
				usage("--cpus has a range that goes down");
		}
		for (cpu = first; cpu <= last; cpu++)
			CPU_SET(cpu, &options->cpus);
		if (!*end)
			return;
		if (*end != ',')
			usage("--cpus needs a list like 0-3,6");
		cursor = end + 1;
	}
}

static void parse_options(int argc, char **argv, Options *options)
{
	int arg_index;

	memset(options, 0, sizeof(*options));
	for (arg_index = 1; arg_index < argc; arg_index++) {
		const char *arg = argv[arg_index];

		if (!strcmp(arg, "--")) {
			arg_index++;
			break;
		}
		if (!strcmp(arg, "--env"))
			add_env(options, option_value(argc, argv, &arg_index));
		else if (!strcmp(arg, "--timeout"))
			options->timeout_sec = parse_seconds(option_value(argc, argv, &arg_index));
		else if (!strcmp(arg, "--cpus"))
			parse_cpus(option_value(argc, argv, &arg_index), options);
		else if (!strcmp(arg, "--stdout"))
			options->stdout_path = option_value(argc, argv, &arg_index);
		else if (!strcmp(arg, "--stderr"))
			options->stderr_path = option_value(argc, argv, &arg_index);
		else if (!strcmp(arg, "--result"))
			options->result_path = option_value(argc, argv, &arg_index);
		else
			usage("unknown option");
	}
	if (arg_index >= argc)
		usage("no command after --");
	options->command = argv + arg_index;
}

static int open_output(const char *path)
{
	int fd;

	if (!path)
		return NO_FD;
	fd = open(path, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, OUTPUT_MODE);
	if (fd < 0)
		fail(path);
	return fd;
}

static long long now_ns(void)
{
	struct timespec now;

	if (clock_gettime(CLOCK_MONOTONIC, &now))
		fail("clock_gettime");
	return now.tv_sec * NSEC_PER_SEC + now.tv_nsec;
}

static int ns_to_poll_ms(long long ns)
{
	long long ms = (ns + NSEC_PER_MSEC - 1) / NSEC_PER_MSEC;

	return ms > INT_MAX ? INT_MAX : (int)ms;
}

static void report_exec_error(int pipe_fd, int error)
{
	while (write(pipe_fd, &error, sizeof(error)) < 0 && errno == EINTR)
		;
}

static int read_exec_error(int pipe_fd)
{
	int error;
	ssize_t got;

	do
		got = read(pipe_fd, &error, sizeof(error));
	while (got < 0 && errno == EINTR);
	return got == (ssize_t)sizeof(error) ? error : 0;
}

static void run_child(const Options *options, int stdout_fd, int stderr_fd, int error_pipe)
{
	struct rlimit no_core = {0, 0};
	int stdin_fd;
	int env_index;

	setpgid(0, 0);
	if (setrlimit(RLIMIT_CORE, &no_core))
		goto failed;
	stdin_fd = open("/dev/null", O_RDONLY | O_CLOEXEC);
	if (stdin_fd < 0 || dup2(stdin_fd, STDIN_FILENO) < 0)
		goto failed;
	if (stdout_fd != NO_FD && dup2(stdout_fd, STDOUT_FILENO) < 0)
		goto failed;
	if (stderr_fd != NO_FD && dup2(stderr_fd, STDERR_FILENO) < 0)
		goto failed;
	for (env_index = 0; env_index < options->env_count; env_index++)
		if (putenv(options->env[env_index]))
			goto failed;
	execvp(options->command[0], options->command);
failed:
	report_exec_error(error_pipe, errno);
	_exit(EXIT_EXEC_FAILED);
}

static bool wait_or_timeout(pid_t pid, double timeout_sec)
{
	struct pollfd exited;
	long long deadline_ns;
	bool timed_out = false;
	int pidfd;

	if (timeout_sec <= 0)
		return false;
	pidfd = (int)syscall(SYS_pidfd_open, pid, 0);
	if (pidfd < 0)
		fail("pidfd_open");
	exited.fd = pidfd;
	exited.events = POLLIN;
	deadline_ns = now_ns() + (long long)(timeout_sec * NSEC_PER_SEC);
	for (;;) {
		long long left_ns = deadline_ns - now_ns();
		int ready;

		if (left_ns <= 0) {
			timed_out = true;
			break;
		}
		ready = poll(&exited, 1, ns_to_poll_ms(left_ns));
		if (ready > 0)
			break;
		if (ready < 0 && errno != EINTR)
			fail("poll");
	}
	close(pidfd);
	return timed_out;
}

static void run(const Options *options, Result *result)
{
	int stdout_fd = open_output(options->stdout_path);
	int stderr_fd = open_output(options->stderr_path);
	int error_pipe[2];
	int status;
	long long start_ns;
	pid_t pid;

	memset(result, 0, sizeof(*result));
	if (options->pin_cpus && sched_setaffinity(0, sizeof(options->cpus), &options->cpus))
		fail("sched_setaffinity");
	if (pipe2(error_pipe, O_CLOEXEC))
		fail("pipe2");

	start_ns = now_ns();
	pid = fork();
	if (pid < 0)
		fail("fork");
	if (!pid)
		run_child(options, stdout_fd, stderr_fd, error_pipe[PIPE_WRITE]);

	setpgid(pid, pid);
	close(error_pipe[PIPE_WRITE]);
	result->exec_errno = read_exec_error(error_pipe[PIPE_READ]);
	close(error_pipe[PIPE_READ]);

	if (wait_or_timeout(pid, options->timeout_sec)) {
		result->timed_out = true;
		kill(-pid, SIGKILL);
		kill(pid, SIGKILL);
	}
	while (wait4(pid, &status, 0, &result->resources) < 0)
		if (errno != EINTR)
			fail("wait4");
	result->wall_ns = now_ns() - start_ns;
	if (result->timed_out)
		kill(-pid, SIGKILL);

	result->exit_code = NO_EXIT_CODE;
	if (WIFEXITED(status))
		result->exit_code = WEXITSTATUS(status);
	else if (WIFSIGNALED(status))
		result->term_signal = WTERMSIG(status);
}

static long long timeval_us(struct timeval time)
{
	return time.tv_sec * USEC_PER_SEC + time.tv_usec;
}

static void write_json_string(FILE *file, const char *text)
{
	fputc('"', file);
	for (; *text; text++) {
		unsigned char symbol = (unsigned char)*text;

		if (symbol == '"' || symbol == '\\')
			fprintf(file, "\\%c", symbol);
		else if (symbol < ' ')
			fprintf(file, "\\u%04x", symbol);
		else
			fputc(symbol, file);
	}
	fputc('"', file);
}

static void write_result(const Options *options, const Result *result)
{
	const struct rusage *resources = &result->resources;
	FILE *file = stdout;

	if (options->result_path) {
		file = fopen(options->result_path, "we");
		if (!file)
			fail(options->result_path);
	}
	fprintf(file, "{\"runner\":%d,\"exit_code\":%d,\"signal\":%d,\"timed_out\":%s,\"exec_error\":",
		PROTOCOL, result->exit_code, result->term_signal, result->timed_out ? "true" : "false");
	write_json_string(file, result->exec_errno ? strerror(result->exec_errno) : "");
	fprintf(file,
		",\"wall_ns\":%lld,\"user_us\":%lld,\"sys_us\":%lld,\"max_rss_kb\":%ld"
		",\"minflt\":%ld,\"majflt\":%ld,\"nvcsw\":%ld,\"nivcsw\":%ld}\n",
		result->wall_ns, timeval_us(resources->ru_utime), timeval_us(resources->ru_stime),
		resources->ru_maxrss, resources->ru_minflt, resources->ru_majflt,
		resources->ru_nvcsw, resources->ru_nivcsw);
	if (fflush(file) || ferror(file))
		fail("write result");
	if (file != stdout && fclose(file))
		fail("close result");
}

int main(int argc, char **argv)
{
	Options options;
	Result result;

	open_standard_fds();
	parse_options(argc, argv, &options);
	run(&options, &result);
	write_result(&options, &result);
	return EXIT_SUCCESS;
}
