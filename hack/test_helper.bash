# Bats supplies errexit but not nounset. Enable nounset in each test process
# without exporting shell options to production scripts launched by the tests.
# A file-local setup() must call strict_setup first; bats-strict-setup.bats
# audits that contract against the Makefile's discovered unit files.
strict_setup() {
  set -u
}

setup() {
  strict_setup
}

# Only a successful matcher explicitly reporting no match passes; missing,
# non-regular or unreadable files and matcher errors fail closed.
assert_file_lacks_pattern() {
  local pattern="$1"
  local file="$2"
  local found

  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
  # Validate even an empty file, and keep the match result separate from awk's
  # status: some implementations also use status 1 for evaluation errors.
  if ! found=$(awk -v pattern="${pattern}" '
    BEGIN { match("", pattern) }
    $0 ~ pattern { found = 1 }
    END { print found ? 1 : 0 }
  ' "${file}"); then
    printf 'awk could not evaluate pattern %s against %s\n' "${pattern}" "${file}" >&2
    return 1
  fi
  if [ "${found}" != 0 ]; then
    printf 'expected %s not to match: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
  return 0
}
