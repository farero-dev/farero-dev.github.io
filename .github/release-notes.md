{{BETA_NOTE}}

## Requirements

- macOS 15 or later (Apple silicon or Intel)
- Claude Code v2.1.203 or later

## Install

1. Download `Farero-{{VERSION}}.zip` below and unzip it.
2. Move `Farero.app` to `/Applications` **before** you open it.
3. Open it. macOS blocks the first launch because the app is not notarized. Go to **System Settings › Privacy & Security** and click **Open Anyway**. Since macOS 15 there is no Control-click bypass.
4. If macOS asks whether farerod may use its Keychain items, enter your password and choose **Always Allow**. It asks again after every update.

Updating: quit Farero, replace the app in `/Applications`, open it.

## Verify the download

```sh
cd ~/Downloads
shasum -a 256 -c Farero-{{VERSION}}.zip.sha256
```

See the [README](https://github.com/farero-dev/farero-dev.github.io#readme) for uninstalling and troubleshooting.

