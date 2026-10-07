# Worker storage bounds

Experimental keyless workers retain the full configured maximum overlay-growth
reservation at startup and before cold boots. `min_free_disk_gib` optionally adds
an operator-selected available-space floor; zero preserves the existing minimum.
These checks stop new admission on pressure and preserve running-job cleanup.
They are admission checks, not atomic filesystem reservations or hard quotas.

`log_retention_mib` optionally bounds retained local diagnostics (0 disables
retention for compatibility; 256 MiB is a reasonable starting operator setting).
The worker removes oldest diagnostics only for durably terminal VMs. Active,
retiring, uncertain and unidentified VM files are protected. Only owned private
regular files named `16hexVMID.jsonl` or `16hexVMID.qemu.log` are eligible; symlinks
and unknown entries fail closed without deletion. Protected files exceeding the
budget stop new admission, rather than interrupting jobs or deleting their logs.
Retention runs during startup, before cold allocation and after confirmed cleanup.
Existing per-file limits remain necessary because active logs are protected.

Hard aggregate storage limits require a separately reviewed dedicated volume or
filesystem quota policy. XFS project quotas and Btrfs subvolume/qgroup accounting
are different mechanisms; neither is enabled automatically. See
[isolation.md](isolation.md) for the trust model and filesystem-specific proposal.
