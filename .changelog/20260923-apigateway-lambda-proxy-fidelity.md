*! [apigateway] Lambda proxy integrations answer AWS's 502 for a malformed response and base64-encode binary request bodies
  a response with no `statusCode`, or `isBase64Encoded: true` with a body that is not base64, used to answer 200 with a guessed body
  migration: return a `statusCode` from the handler, and base64-encode the body whenever `isBase64Encoded` is true
