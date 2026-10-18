# Assurance

What a plan's security, compliance and clinical safety reviewers ask for first, as far as a codebase can answer it.
None of it has been reviewed by anyone outside BioDream; it is the material for that review, not its result.

- [Threat model](threat-model.md): what is protected, from whom, by what in the code, and what is left to the plan.
- [HIPAA Security Rule](hipaa-security-rule.md): 45 CFR 164.312's technical safeguards, mapped to the code.
- [Hazard log](hazard-log.md): ways the decision logic could harm a patient, the controls against each, and their tests.
- [Recovery, measured](recovery.md): what a crash and a standby takeover lose and take, from the recovery drill.

Checkable by anyone, in the repository's Actions tab: tests on Linux, macOS and Windows; the recovery drill;
govulncheck and npm audit; CodeQL; fuzzing; OWASP ZAP's baseline scan; the OpenSSF Scorecard; the installers and the
Helm chart on fresh machines. Release archives are reproducible and carry SBOMs and build provenance.

Not here, and not claimed: SOC 2 or HITRUST reports, an independent penetration test, a business associate agreement,
service levels, certification by anyone, or clinical validation in production. BioDream does not operate Vaglio for
anyone and offers no managed service.
