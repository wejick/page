## REMOVED Requirements

### Requirement: Minimal upload UI
**Reason**: The upload-only form is replaced by the full management UI
(list, detail, and upload views), which is now specified by the `admin-ui`
capability. The upload API contract is unchanged.
**Migration**: The upload form lives on as the upload view of the management
UI at `/` with identical behavior (same fields, same success/error display);
no user action required.
