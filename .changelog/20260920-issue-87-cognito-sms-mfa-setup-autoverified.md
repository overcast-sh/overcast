* cognito Cognito now issues and completes SMS_MFA, SELECT_MFA_TYPE and MFA_SETUP challenges.
  SetUserMFAPreference and AdminSetUserMFAPreference accept SMSMfaSettings, and GetUser/AdminGetUser report UserMFASettingList and PreferredMfaSetting.
  AssociateSoftwareToken and VerifySoftwareToken accept an MFA_SETUP challenge Session, so a user can enrol a software token mid-sign-in.
* cognito UpdateUserAttributes now sends a code for an attribute in the pool AutoVerifiedAttributes.
  CreateUserPool, UpdateUserPool and DescribeUserPool store and return AutoVerifiedAttributes, which the service previously dropped, and SignUp returns CodeDeliveryDetails.
  CodeDeliveryDetails destinations are now masked the way AWS masks them.
