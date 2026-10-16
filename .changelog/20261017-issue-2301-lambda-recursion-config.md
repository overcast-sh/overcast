+ [lambda] `PutFunctionRecursionConfig` and `GetFunctionRecursionConfig` store a function's `RecursiveLoop` setting, `Allow` or `Terminate` (#2301).
  The default is `Terminate`. Overcast runs no recursive-loop detection, so the setting is stored and echoed.
~ [cloudformation] `AWS::Lambda::Function`'s `RecursiveLoop` is applied through that setting instead of failing the stack.
