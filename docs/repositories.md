# Repository ownership and agent entry points

| Repository | Responsibility | Visibility |
| --- | --- | --- |
| [chickadee](https://github.com/plover-digital/chickadee) | Self-hostable VM/runner host software, bootstrap, images, local scheduling, lifecycle and standalone GitHub integration | Public |
| [chickadee-roost](https://github.com/plover-digital/chickadee-roost) | Service manager, website/OAuth, customer queues, admission, quotas, aggregated usage and future fleet routing | Public; independent Go module |
| chickadee-ops | Deployment configuration and private operational runbooks | Private; inventory is maintained in internal Notion |

Start at each repository's root `AGENTS.md`, then its README and relevant design
or operations document. The same ownership pattern applies to new projects:
public independently deployable engine, separately deployable service layer,
private deployment operations. Do not create another repository merely for
shared types: the host repository can expose a small versioned API/client.

## Extraction status

The website, OAuth, admission bridge, service policy example and service-specific
systemd units have moved to Roost's independently buildable public module.
Executable, environment and state paths are retained initially for deployment
compatibility. This extraction does not implement fleet scheduling or change
customer GitHub labels. The controller's GitHub adapter and standalone App setup
helper remain available for independent self-host deployment. The versioned public `workerapi` and keyless `chickadee-worker` are implemented;
Roost owns experimental multi-host placement and GitHub demand listeners.

## Choosing where to work

- VM boot, READY/CONFIG/ACK, QEMU, disk cleanup, guest image packages or host
  resource enforcement: Chickadee.
- Login, installation/enrollment, queue-selection UI, beta approval, service
  status, user usage or provider routing: Roost.
- Actual hostnames, addresses, customer policy, credential storage references,
  deployment/rollback and capacity inventory: private operations.

The VM engine must not import the service repository. The service uses the public
host API/client, not Go `internal` VM packages, host paths or arbitrary shell
commands. The admission bridge manages customer policy; keyless workers manage
shared physical warm capacity.

Public issues describe implementation and generic acceptance. Private issues and
Notion describe deployment/customer specifics. Never store secret values in
either tracker. State which version was built and actually deployed; distinguish
planned interfaces from working code.
