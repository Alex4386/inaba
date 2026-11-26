# ezmode

> [!WARNING]  
> ezmodo? Kimoi~ ♡  
> <img width="480" height="360" alt="Kimoi~" src="https://github.com/user-attachments/assets/8eac00ae-c441-464b-ae00-c1276c501d76" />

Fine. You don't know how to set up systemd, and you don't want to babysit `inaba connect` in a terminal forever. Nobody's judging. (We are.)

```sh
inaba login --server https://inaba.example.com   # if you haven't yet
inaba ezmode install                              # yes, `inaba ezmodo install` works too
```

That's it. The host now runs in the background, and comes back after a reboot.

## What it actually does

The easiest thing your OS has, so you don't have to learn it:

| OS | What you get | Where it is | Its log |
| --- | --- | --- | --- |
| Linux | a systemd user service, plus lingering so it keeps running after you log out and starts at boot | `~/.config/systemd/user/inaba.service` | `journalctl --user -u inaba` |
| macOS | a launchd agent, which runs while you're logged in | `~/Library/LaunchAgents/io.github.alex4386.inaba.plist` | `~/Library/Logs/inaba.log` |
| Windows | a Task Scheduler task started when you sign in, in a headless console so no window stares at you | the `inaba` task in Task Scheduler | `inaba.log` beside the database, in `%LocalAppData%\inaba` |

It runs **this** `inaba` binary, as you, with the same settings and sign-in `inaba connect` would use. If you passed `--config`, `--data` or `--socket`, those go along too.

## Things it takes along, and things it doesn't

- **Your shell's `PATH`** (Linux and macOS), so the host finds the same `claude`, `npx` and friends you do. Installed an agent somewhere new? Run `inaba ezmode install` again.
- **Not your `INABA_` variables.** They might be secrets, and it isn't writing those into a file for you. It tells you which ones it skipped; put them in your [settings file](./CONFIGURATION.md), or on Linux in `~/.config/inaba/host.env`.

## Running it again

Moved `inaba`, installed new agents, changed your mind about flags? Run `inaba ezmode install` again. It writes the setup afresh and restarts the host, and since that ends any turn that's running, it asks first. `-y` doesn't.

`inaba update` and `inaba restart` work as usual; the host comes back in place.

## Getting rid of it

```sh
inaba ezmode uninstall
```

Stops the host and removes what `install` set up. Your settings, sign-in, projects and threads stay, so `inaba connect` (or another `install`) picks up where you left off. On Linux, lingering stays on, since something else might want it: `loginctl disable-linger` if not.

## When it doesn't work

- **"this machine is not signed in"**: `inaba login --server …` first. It can't approve itself.
- **"a host is already running here"**: you have `inaba connect` open in a terminal somewhere. Close it, then install.
- **Linux: "no systemd user session here"**: WSL without systemd, a container, or you got here with `su`. Log in properly, or run `inaba connect` under whatever does start your programs.
- **Linux: lingering didn't turn on**: some systems want an admin for that. `sudo loginctl enable-linger $USER`, once. Until then it runs only while you're logged in.
- **macOS over SSH**: launchd agents live in your login session, and SSH alone doesn't have one. Log in at the Mac once.
- **Windows: "Access is denied"**: some Windows setups let only administrators add a task that starts at sign-in. Run it once from a terminal opened as administrator; the host still runs as you, not as an administrator.
- **It started, then didn't**: read its log (see the table). It's usually a `PATH` that doesn't find your agents, or a settings file it didn't like.

## Grown-up mode

Want to know what it wrote? It's all plain files in the places above, with a comment on top saying `inaba ezmode install` wrote them. Edit them by hand if you like, but the next `install` writes them afresh. If you'd rather write your own, the systemd unit in [contrib/systemd](../contrib/systemd) has how.
