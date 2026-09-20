* [dynamodb] Query key conditions are checked against the key schema in play, and may be written in either order and in parentheses.
  A second condition on a non-key attribute used to be answered as if it named the sort key; a sort-key condition written before the
  partition key, and any parentheses, used to be rejected outright. Two conditions on one key are now AWS's "KeyConditionExpressions
  must only contain one condition per key". (#135)
