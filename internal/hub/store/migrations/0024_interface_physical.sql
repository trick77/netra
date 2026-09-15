-- The interface table listed a bond next to the NICs under it and gave no way
-- to tell them apart short of knowing the naming scheme.
--
-- The agent now reports whether /sys/class/net/<iface>/device exists: every
-- netdev on a bus (PCI, USB, platform) has the link and nothing the kernel
-- made up -- bond, bridge, VLAN, dummy, macvlan -- does. The symlink's
-- presence is stored as reported; the classification stays out of the agent
-- the way it does for oper_state.

-- NULL for a row an older agent wrote, or one from a host whose sysfs could
-- not be read. Overwritten on every report like the other link attributes:
-- the agent sends the whole set, and there is no reading that carries the
-- other fields and not this one.
ALTER TABLE host_interfaces ADD COLUMN IF NOT EXISTS physical BOOLEAN;
