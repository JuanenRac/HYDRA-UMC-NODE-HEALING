# Security Policy 🔒 (HYDRA-UMC-NODE-HEALING)

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.x.x  | ✅ Yes             |

## Reporting a Vulnerability

**CRITICAL: Do not report safety-critical vulnerabilities through public GitHub issues.**

In a high-availability system, a security flaw can be used to cause a "denial of motion" or fake failover loops. If you discover a vulnerability affecting the **heartbeat integrity**, **remote reboot bypasses**, or **failover priority hijacking**:

1. **Email**: Send a detailed report to `electrohobby3d@gmail.com`.
2. **Impact**: Describe if the bug allows triggering unauthorized hardware resets, spoofing node health, or preventing mission failover during real failures.
3. **Response**: Initial acknowledgment within 48 hours.

We follow a coordinated disclosure policy to ensure hardware safety before public release.
