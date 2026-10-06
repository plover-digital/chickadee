# Managed service integration

The website and customer admission bridge now live in the separate public
[Chickadee Roost repository](https://github.com/plover-digital/chickadee-roost).
See [the Roost documentation](https://github.com/plover-digital/chickadee-roost/blob/main/docs/managed-beta.md)
for configuration, installation and operational behavior.

Chickadee remains independently deployable without Roost. Its standalone GitHub
adapter, App setup helper, VM lifecycle, images and host resource limits remain
in this repository. The service-to-host API and fleet scheduling are planned;
the existing single-host admission bridge is a migration adapter.
