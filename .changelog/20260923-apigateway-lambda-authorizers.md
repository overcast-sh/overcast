+! [apigateway] enforce Lambda TOKEN/REQUEST authorizers (REST v1) and Lambda REQUEST authorizers (HTTP v2)
  requests to a method or route with a Lambda authorizer were let through unchecked; they now reach the integration only when the authorizer allows them
  AWS_IAM authorizers remain unenforced
  migration: make the authorizer function return an Allow policy (or `isAuthorized: true`) for the requests your tests make, or remove the authorizer from the local deployment
