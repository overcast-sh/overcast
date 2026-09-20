* cognito Cognito now issues and completes SMS_MFA, SELECT_MFA_TYPE and MFA_SETUP challenges.
  SetUserMFAPreference and AdminSetUserMFAPreference accept SMSMfaSettings, and GetUser/AdminGetUser report UserMFASettingList and PreferredMfaSetting.
  AssociateSoftwareToken and VerifySoftwareToken accept an MFA_SETUP challenge Session, so a user can enrol a software token mid-sign-in.
