# Booth setup guide

One booth PC per outlet. The agent is the only moving part on the machine; Chrome
in kiosk mode is the only other process the customer touches.

Everything here is once per machine. If Windows is reinstalled or the PC is
replaced, do it again from the top — and the one thing that undoes itself
without help is the driver swap in step 2.

## Windows — the production booth

### Before the install script

**1. gphoto2 on PATH.** Windows has no official gphoto2 build, so this is the
step people get stuck on: the agent shells out to `gphoto2.exe` and does not
ship one. Build or obtain it however you can (an MSYS2 build is the usual
route), and make sure `gphoto2 --version` prints a version from an ordinary
PowerShell, not only from inside the MSYS2 shell.

If you would rather not deal with gphoto2 at all, the booth can fire the camera
through digiCamControl instead, with `-shutter http://127.0.0.1:5513/?slc=capturenoaf`.
That uses Canon's own driver, so it needs no driver swap either — but the
install script covers the gphoto2 path, so that one is set up by hand.

**2. WinUSB driver swap (Zadig).** Canon's PTP driver stops libgphoto2 from
seeing the camera.

- Download Zadig, run it, **Options → List All Devices**.
- Pick the Canon in the list, choose **WinUSB**, and replace the driver.
- **Signal:** `gphoto2 --auto-detect` prints a model and a port.
- **What undoes it:** a Windows update, or installing Canon software, can
  restore the PTP driver without saying anything. A booth that worked for weeks
  and then stopped seeing the camera is this, nine times out of ten. Run Zadig
  again.

**3. Camera in PC Connection mode.** The Canon's USB setting must not be
charge-only.

- **Signal:** the camera appears in Zadig's device list.

**4. The printer, and two queues on it.** One queue for whole sheets, one with
the 2-inch cut enabled, because the driver holds the cut as a machine-wide
setting rather than per job.

- **Signal:** `Get-Printer -Name "DS-RX1"` and `Get-Printer -Name "DS-RX1 cut"`
  both return a queue.

**5. The timezone.** Every session and frame is timestamped, and the seven-day
purge counts from those timestamps.

- **Signal:** `Get-TimeZone` shows W. Indonesia Time.

### Run the install script

In an **elevated** PowerShell:

```powershell
Set-ExecutionPolicy -ExecutionPolicy Bypass -Scope Process -Force
$p = "$env:TEMP\install-booth.ps1"
Invoke-RestMethod -Uri https://raw.githubusercontent.com/yudhabhaktin/bykami/main/deploy/booth/install-booth.ps1 -OutFile $p
Unblock-File $p
& $p
```

Both the execution policy and `Unblock-File` are there because either one alone
can stop the script: a file downloaded from the internet is marked as such, and
PowerShell refuses to run it until that mark is cleared.

The script picks the newest `agent-*` release, verifies the SHA256 digest,
installs the binary under `Program Files\Bykami`, registers the service **with
the booth flags it was given**, and finishes by running `bykami-agent doctor`.

Preview it without changing anything:

```powershell
& $p -DryRun
```

### After the install script

**6. Chrome in kiosk mode.** The service does not start Chrome — a service runs
outside any logon session. Log in as the booth user, set Chrome to open
`http://localhost:8899` in kiosk mode, and put a shortcut in the Startup folder.

- **Signal:** rebooting the machine lands on the booth's attract screen with no
  keyboard input.

**7. Assigned Access.** Lock that account to Chrome alone, so a customer cannot
reach the desktop.

**8. The hot folder.** The script creates `C:\ProgramData\Bykami\booth\hot`.
With `-camera-tool gphoto2` the agent downloads captured frames into it itself
and nothing else needs to write there. Only if you tether with Canon's EOS
Utility instead do you point that application's destination at this directory —
and then it owns the camera, so gphoto2 cannot also be firing it.

## Linux — a booth except printing

Linux runs everything except printing: there is no backend other than `sim`,
because the spooler is Windows-only. A Linux *VPS* proves nothing about the
camera either — it has no USB bus — so this section is for a Linux machine in
the room.

```bash
curl -fsSL https://raw.githubusercontent.com/yudhabhaktin/bykami/main/deploy/booth/setup-booth.sh | bash
```

The script asks before installing gphoto2 through `apt`, `dnf` or `brew`,
writes the udev rule, downloads the agent, verifies the SHA256, and runs the
doctor.

Manual steps it cannot do for you:

- **Log out and back in** after the script adds you to `plugdev`. A group
  membership that has not been applied looks exactly like a broken camera.
  - **Signal:** `bykami-agent doctor` reports *present and claimable* without
    `sudo`.
- **The camera in PC Connection mode**, as on Windows.
- **The timezone**: `timedatectl set-timezone Asia/Jakarta`.

## macOS — development only

macOS cannot be a production booth: there is no Assigned Access equivalent, and
no printer backend. It is the easiest place to prove the camera path and work on
the kiosk UI.

```bash
curl -fsSL https://raw.githubusercontent.com/yudhabhaktin/bykami/main/deploy/booth/setup-booth.sh | bash
```

The script installs gphoto2 through Homebrew, puts the agent in `~/.local/bin`,
and runs the doctor. If `bykami-agent` is not found afterwards, `~/.local/bin`
is not on your PATH.

Manual steps:

- **Quit Image Capture.** macOS opens it when a Canon is plugged in and it holds
  the PTP device, so gphoto2 cannot claim it.
  - **Signal:** `bykami-agent doctor` reports *present and claimable*.
  - **What breaks it:** plugging the camera in again, or Photos or any other
    photo application grabbing the device first.

## The single signal for each step

If a signal fails, stop there — the next step will not fix it. `bykami-agent
doctor` is the one command to run when something that used to work does not.

| Step | Platform | Signal |
|---|---|---|
| gphoto2 installed | all | `gphoto2 --version` prints a version from a normal shell |
| WinUSB swap | Windows | `bykami-agent doctor` reports *present and claimable* |
| PC Connection mode | all | the camera appears in `gphoto2 --auto-detect` |
| udev rule in effect | Linux | the doctor reports *present and claimable* without `sudo` |
| Image Capture quit | macOS | the doctor reports *present and claimable* |
| Printer queues | Windows | `Get-Printer` returns both queue names |
| Service running | Windows | `Get-Service bykami-agent` shows Running |
| Booth flags on the service | Windows | `sc qc bykami-agent` shows the booth's path and arguments |
| Timezone | all | `Get-TimeZone`, `timedatectl` or `date` shows WIB |
| Updating itself | all | the log line *update available* or *healthy*, once a newer release exists |

## What the install script checks, and what it does not

It checks the SHA256 digest of the downloaded binary against the release's
`.sha256` file, and stops on mismatch. It does **not** verify the ed25519
signature: PowerShell cannot do that without a component Windows does not ship.
The signature is verified by the agent itself on every self-update, against the
public key compiled into the binary. So the very first hop — script to GitHub —
is integrity-checked but not authenticated, and closing that gap needs an
Authenticode certificate, which has not been bought.
