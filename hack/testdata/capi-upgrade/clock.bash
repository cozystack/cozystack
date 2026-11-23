sleep() {
  if [[ -n "${CAPI_TEST_CLOCK_JUMP:-}" ]]; then
    SECONDS=$((SECONDS + CAPI_TEST_CLOCK_JUMP))
  else
    command sleep "$@"
  fi
}
