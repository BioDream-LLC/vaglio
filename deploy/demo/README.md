# vaglio.perfuse.health

A public Vaglio for anyone to look at, on invented requests only. Nothing on it is about a real person.

| What | Where |
|---|---|
| Console | `https://vaglio.perfuse.health/`, username `demo`, password `try-vaglio-demo` (look at everything, change nothing) |
| Check a receipt | `https://vaglio.perfuse.health/#/receipt` |

An invented lumbar spine MRI request arrives every 20 minutes (`traffic.py`): the same synthetic patient, the policy's
questions answered at random, some left unanswered. Every night at 04:00 UTC the log is cleared and a day's worth of
requests is sent again, so the queue never fills with overdue cases. The demo account is a viewer: it can open cases,
try policies, use the what-if sandbox, read letters and check receipts, and can decide or change nothing.

```sh
deploy/demo/provision.sh   # create or upgrade: Lightsail nano, static IP, DNS, certificate, service, traffic
```

The instance is about $5 a month. SSH is open only to the address `provision.sh` ran from.
