def int64:
  if type != "string" or (test("^[+-]?[0-9]+$") | not) then null
  else
    startswith("-") as $negative |
    sub("^[+-]"; "") | sub("^0+"; "") |
    (if . == "" then "0" else . end) as $digits |
    (if $negative then "9223372036854775808" else "9223372036854775807" end) as $limit |
    if [($digits | length), $digits] > [19, $limit] then null
    else {negative: ($negative and $digits != "0"), digits: $digits} end
  end;
def compareIntegers($left; $right):
  ($left | int64) as $a | ($right | int64) as $b |
  if $a == null or $b == null then null
  elif $a.negative != $b.negative then (if $a.negative then -1 else 1 end)
  elif $a.digits == $b.digits then 0
  else
    (if [($a.digits | length), $a.digits] > [($b.digits | length), $b.digits] then 1 else -1 end) *
    (if $a.negative then -1 else 1 end)
  end;
def requirement($values):
  . as $r | $values[$r.key] as $v |
  if .operator == "In" then $v != null and (.values | index($v)) != null
  elif .operator == "NotIn" then (.values | index($v)) == null
  elif .operator == "Exists" then $v != null
  elif .operator == "DoesNotExist" then $v == null
  elif .operator == "Gt" then compareIntegers($v; .values[0]) as $n | $n != null and $n > 0
  elif .operator == "Lt" then compareIntegers($v; .values[0]) as $n | $n != null and $n < 0
  else error("unsupported PV node affinity operator: " + .operator)
  end;
def permits($node):
  .spec.nodeAffinity.required as $required |
  $required == null or any($required.nodeSelectorTerms[]?;
    ((.matchExpressions // []) + (.matchFields // []) | length) > 0 and
    all(.matchExpressions[]?; requirement($node.metadata.labels // {})) and
    all(.matchFields[]?; requirement({"metadata.name": $node.metadata.name})));
[.items[] |
  select(.spec.unschedulable != true) |
  select(any(.status.conditions[]?; .type == "Ready" and .status == "True")) |
  . as $node | select(all($volumes[]; permits($node))) |
  .metadata.name] | sort | first // error("no ready node is compatible with every GPU VM disk")
