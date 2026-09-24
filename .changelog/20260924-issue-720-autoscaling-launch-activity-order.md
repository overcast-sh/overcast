* [autoscaling] an instance no longer shows `InService` while its launch activity still reads `PreInService`.
  the launch activity is now marked `Successful` before the instance goes `InService`, so `DescribeAutoScalingInstances` and `DescribeScalingActivities` always agree.
