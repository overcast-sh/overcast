* [stepfunctions] `RedriveExecution` is accepted the instant an execution reads terminal, instead of sometimes refusing it as already being redriven.
  a distributed Map also starts no further child once a failure has passed its tolerance, rather than racing the child that ended the run
