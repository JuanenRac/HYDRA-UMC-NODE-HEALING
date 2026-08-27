# Contributing to HYDRA-UMC-NODE-HEALING 🦾

We welcome contributions to the resilience and failover manager of the HYDRA-UMC platform.

## Technology Stack
- **Languages**: Rust 1.80+, Go 1.22+.
- **Monitoring**: gRPC Heartbeats, SNMP, Thermal Telemetry.
- **Protocols**: mTLS for secure node heartbeats.
- **Architecture**: Distributed Failover Logic.

## Guidelines
1. **Low Overhead**: The health monitor must use minimal CPU/Network resources to avoid impacting real-time control.
2. **Failover Determinism**: Failover logic must be idempotent and deterministic to prevent mission duplication.
3. **Security**: Ensure that node healing commands are authenticated via mTLS to prevent spoofed heartbeats.
4. **Testing**: Validate failover scenarios using the `HYDRA-UMC-HIL-BRIDGE` by simulating hardware disconnects.
