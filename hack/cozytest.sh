#!/bin/sh
###############################################################################
# cozytest.sh - Bats-compatible test runner with live trace and enhanced      #
# output, written in pure shell                                               #
###############################################################################
set -eu

TEST_FILE=${1:?Usage: ./cozytest.sh <file.bats> [pattern]}
PATTERN=${2:-*}
LINE='----------------------------------------------------------------'

cols() { stty size 2>/dev/null | awk '{print $2}' || echo 80; }
MAXW=$(( $(cols) - 12 )); [ "$MAXW" -lt 40 ] && MAXW=70
BEGIN=$(date +%s)
timestamp() { s=$(( $(date +%s) - BEGIN )); printf '[%02d:%02d]' $((s/60)) $((s%60)); }

###############################################################################
# run_one <fn> <title>                                                        #
###############################################################################
run_one() {
  fn=$1 title=$2
  tmp=$(mktemp -d) || { echo "Failed to create temp directory" >&2; exit 1; }
  log="$tmp/log"

  echo "╭ » Run test: $title"
  START=$(date +%s)
  skip_next="+ $fn"

  {
    (
      PS4='+ '           # prefix for set -x
      set -eu -x         # strict + trace
      "$fn"
    )
    printf '__RC__%s\n' "$?"
  } 2>&1 | tee "$log" | while IFS= read -r line; do
        case "$line" in
          '__RC__'*) : ;;
          '+ '*)   cmd=${line#'+ '}
                    [ "$cmd" = "${skip_next#+ }" ] && continue
                    case "$cmd" in
                      'set -e'|'set -x'|'set -u'|'return 0') continue ;;
                    esac
                    out=$cmd ;;
          *)       out=$line ;;
        esac
        now=$(( $(date +%s) - START ))
        [ ${#out} -gt "$MAXW" ] && out="$(printf '%.*s…' "$MAXW" "$out")"
        printf '┊[%02d:%02d] %s\n' $((now/60)) $((now%60)) "$out"
  done

  rc=$(awk '/^__RC__/ {print substr($0,7)}' "$log" | tail -n1)
  [ -z "$rc" ] && rc=1
  now=$(( $(date +%s) - START ))

  if [ "$rc" -eq 0 ]; then
    printf '╰[%02d:%02d] ✅ Test OK: %s\n' $((now/60)) $((now%60)) "$title"
  else
    printf '╰[%02d:%02d] ❌ Test failed: %s (exit %s)\n' \
           $((now/60)) $((now%60)) "$title" "$rc"
    echo "----- captured output -----------------------------------------"
    grep -v '^__RC__' "$log"
    echo "$LINE"
    exit "$rc"
  fi

  rm -rf "$tmp"
}

###############################################################################
# convert .bats -> shell-functions                                            #
###############################################################################
TMP_SH=$(mktemp) || { echo "Failed to create temp file" >&2; exit 1; }
trap 'rm -f "$TMP_SH"' EXIT
awk '
  /^@test[[:space:]]+"/ {
    line  = substr($0, index($0, "\"") + 1)
    title = substr(line, 1, index(line, "\"") - 1)
    fname = "test_"
    for (i = 1; i <= length(title); i++) {
      c = substr(title, i, 1)
      fname = fname (c ~ /[A-Za-z0-9]/ ? c : "_")
    }
    printf("### %s\n", title)
    printf("%s() {\n", fname)
    print "  set -e"
    next
  }
  /^}$/ {
    print "  return 0"
    print "}"
    next
  }
  { print }
' "$TEST_FILE" > "$TMP_SH"

[ -f "$TMP_SH" ] || { echo "Failed to generate test functions" >&2; exit 1; }
# shellcheck disable=SC1090
. "$TMP_SH"

###############################################################################
# run selected tests                                                          #
###############################################################################
awk -v pat="$PATTERN" '
  /^### / {
    title = substr($0, 5)
    name = "test_"
    for (i = 1; i <= length(title); i++) {
      c = substr(title, i, 1)
      name = name (c ~ /[A-Za-z0-9]/ ? c : "_")
    }
    if (pat == "*" || index(title, pat) > 0)
      printf("%s %s\n", name, title)
  }
' "$TMP_SH" | while IFS=' ' read -r fn title; do
  run_one "$fn" "$title"
done
