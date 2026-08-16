import { describe, expect, it } from "vitest";
import { OS_ICON_PATHS, osIcon } from "./osIcon";

describe("osIcon", () => {
  // Real ID/PRETTY_NAME strings out of /etc/os-release, because the whole
  // risk in this table is ORDER: every one of these contains "linux", and a
  // table that tested the generic pattern first would draw Tux on the entire
  // fleet.
  it.each([
    ["Ubuntu 24.04.1 LTS", "ubuntu"],
    ["Debian GNU/Linux 13 (trixie)", "debian"],
    ["Alpine Linux v3.19", "alpinelinux"],
    ["AlmaLinux 9.3 (Shamrock Pampas Cat)", "almalinux"],
    ["Rocky Linux 9.3 (Blue Onyx)", "rockylinux"],
    ["Red Hat Enterprise Linux 9.3 (Plow)", "redhat"],
    ["Fedora Linux 39 (Server Edition)", "fedora"],
    ["Arch Linux", "archlinux"],
    ["openSUSE Leap 15.5", "opensuse"],
    ["Linux Mint 21.3", "linuxmint"],
  ])("draws %s as %s, not as the generic Tux", (name, expected) => {
    expect(osIcon(name)).toBe(expected);
  });

  // The GOOS fallback an agent reports when it could not read /etc/os-release
  // at all. Tux is exactly right here -- it is a Linux and nothing more is
  // known about it.
  it("falls back to Tux for a bare GOOS linux", () => {
    expect(osIcon("linux")).toBe("linux");
  });

  it.each([
    ["darwin", "apple"],
    ["macOS 15.1", "apple"],
    ["freebsd", "freebsd"],
  ])("knows %s", (name, expected) => {
    expect(osIcon(name)).toBe(expected);
  });

  // No icon, rather than a question-mark glyph: an unrecognised distribution
  // is not a distribution called "unknown", and a placeholder in a line this
  // quiet is worse than a name standing on its own.
  it.each([null, "", "Windows Server 2022", "Plan 9"])(
    "draws nothing for %s",
    (name) => {
      expect(osIcon(name)).toBeNull();
    },
  );

  // Every key the matcher can return has to have a path behind it, or the
  // component renders <path d={undefined}> and the mark silently vanishes on
  // exactly the distributions nobody on this team runs.
  it("has a path for every key it can return", () => {
    const names = [
      "Ubuntu",
      "Linux Mint",
      "Debian",
      "AlmaLinux",
      "Rocky Linux",
      "Red Hat",
      "Fedora",
      "Alpine",
      "Arch Linux",
      "openSUSE",
      "FreeBSD",
      "darwin",
      "linux",
    ];
    for (const name of names) {
      const key = osIcon(name)!;
      expect(key).not.toBeNull();
      expect(OS_ICON_PATHS[key]).toBeTruthy();
    }
    // And nothing unreachable is being shipped.
    expect(Object.keys(OS_ICON_PATHS).length).toBe(names.length);
  });
});
