* [cognito] SetUserPoolMfaConfig stores and returns SmsMfaConfiguration, SoftwareTokenMfaConfiguration and EmailMfaConfiguration.
  the three factor configurations were parsed and dropped, so GetUserPoolMfaConfig answered with MfaConfiguration alone.
  the request now replaces the MFA configuration as a unit, and is rejected when it enables MFA with no factor or turns MFA off while configuring one.
