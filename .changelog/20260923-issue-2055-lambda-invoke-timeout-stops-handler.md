* [lambda] a timed-out invocation's handler is stopped immediately, not left running past REPORT
  the container is killed at the deadline rather than after Release, so late output and side effects can no longer happen; the next invoke gets a fresh environment
