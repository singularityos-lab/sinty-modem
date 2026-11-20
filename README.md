# sinty-modem

The cellular modem service for Sinty OS. It brings up the phone's baseband, registers
on the network, and serves the ModemManager D-Bus API, so the Phone and Messages apps,
and any tool written against `org.freedesktop.ModemManager1`, work without
ModemManager itself.

The first supported modem is the Samsung Shannon baseband in Google Tensor G2 phones
(Pixel 7 Pro, `google,gs201-cheetah`): VoLTE calls, SMS over IMS, mobile data and SIM
PIN entry.

## How it works

`sinty-modemd stack` loads the modem link drivers listed in the device profile and
boots the baseband with the vendor's own modem tools, read at runtime from the phone's
`super` partition in a private mount namespace. No vendor file is shipped or copied
to disk. The modem's NV partitions are used only through copies in memory and are
never written. A bring-up that keeps failing stops after three attempts instead of
resetting the modem in a loop.

`sinty-modemd` talks to the booted modem over its IPC channel: SIM status and PIN,
network attach with the APN from mobile-broadband-provider-info, the internet and IMS
bearers, IMS registration, calls with the call audio route, and SMS, which is stored
on disk before the network gets its acknowledgement. The internet bearer gets its own
`rmnet` link with a default route behind Wi-Fi, and its DNS servers are handed to
sinty-nm through `/run/sinty/dns.d`.

The device profile is `/usr/share/sinty/devices/<compatible>/modem.conf`.

## Build

    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o sinty-modemd ./cmd/sinty-modemd

## Use of Generative AI

Maintainers may use generative AI tools as assistants while working on sinty-modem. Non-trivial assisted commits disclose the tool, model, and scope of the work.

AI tools may assist with code comments, documentation, repetitive code, and issue triage. Maintainers make project decisions and review every assisted change before it is merged.

Use these trailers for non-trivial assisted commits:

```plain
Assisted-by: <tool>:<model-version>
AI-Scope: <what the tool generated and the prompt or a short prompt summary>
```

Single-line completions, renames, and formatting changes do not need trailers.

Coding agents must also follow [AGENTS.md](AGENTS.md) before changing files,
creating commits, or opening pull requests.

## License

GPL-3.0. The modem bring-up sequence and message layouts were learned from
[omarchy-mobile](https://github.com/derekross/omarchy-mobile) (MIT); see `NOTICE`.
