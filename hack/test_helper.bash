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

assert_file_lacks_pattern() {
  local pattern="$1"
  local file="$2"
  local _rc=0

  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
  # Validate the regex even for an empty file. Only status 1 means no match;
  # keeping awk in an OR-list lets that normal result survive errexit.
  awk -v pattern="${pattern}" '
    BEGIN { match("", pattern) }
    $0 ~ pattern { found = 1 }
    END { exit found ? 0 : 1 }
  ' "${file}" || _rc=$?
  case "${_rc}" in
    0)
      printf 'expected %s not to match: %s\n' "${file}" "${pattern}" >&2
      return 1
      ;;
    1) return 0 ;;
    *)
      printf 'awk could not evaluate pattern %s against %s\n' "${pattern}" "${file}" >&2
      return 1
      ;;
  esac
}
