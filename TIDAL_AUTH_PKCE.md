# Tidal Authentication (PKCE Web Flow)

Due to recent changes in Tidal's OAuth API (March 2026), the traditional "Device Flow" (using legacy TV or Android Auto Client IDs) has been severely restricted. Tidal now actively strips the `playback` scope from these tokens, preventing direct FLAC downloads even if you authenticate with a Premium account.

To solve this, SpotiFLAC implements the **PKCE (Proof Key for Code Exchange) Web Flow**, perfectly mimicking the official Tidal Web Player (`listen.tidal.com`).

> **⚠️ IMPORTANT:** You **MUST** have an active, paid Tidal subscription for this to work. Free accounts will not be granted the `playback` scope. If you don't have an account, SpotiFLAC will automatically fallback to community-hosted APIs.

## How to Authenticate

You can authenticate using the provided automated script or manually via `curl`.

### Option A: Using the Automated Script (Recommended)

We have provided a Python script that automates the entire process. Simply run it from any terminal that has Python installed:

```bash
python3 auth_tidal.py --host http://<YOUR-SPOTIFLAC-IP>:6890
```
*(If you have a local password configured on SpotiFLAC, add: `--password YOUR_PASSWORD`)*

The script will request the secure login URL, attempt to open your browser, and prompt you to paste the final redirected URL. It handles all the API exchanges for you.

### Option B: Manual Method (Step-by-Step)

If you prefer not to use the Python script or are in a restricted headless environment, you can perform the authentication manually using `curl`.

#### Step 1: Generate the Login URL
Request a secure login URL (containing a cryptographic `code_challenge`) from your SpotiFLAC instance. Run this command on your host machine:

```bash
curl http://<YOUR-SPOTIFLAC-IP>:6890/api/auth/tidal/url
```
*(If you have a local password configured, add: `-H "Authorization: Bearer YOUR_PASSWORD"`)*

This will return a JSON response containing a long URL:
```json
{
  "url": "https://login.tidal.com/authorize?appMode=web&client_id=txNoH4kkV41MfH25&code_challenge=...&response_type=code..."
}
```

#### Step 2: Log in via your Browser
Copy the `url` string from the JSON response and open it in your standard web browser (Chrome, Firefox, Safari, etc.).
Log in using your Premium Tidal account. This bypasses any bot-protection or CAPTCHAs since you are using a real browser.

#### Step 3: Copy the Redirect URL
After a successful login, Tidal will redirect you. You might see a blank page or be redirected to the web player.
Look at your browser's address bar. The URL should look something like this:

`https://listen.tidal.com/login/auth?code=abc123def456ghi789&lang=en`

**Copy this entire URL.**

#### Step 4: Send the Code to SpotiFLAC
Send the copied URL back to SpotiFLAC so it can securely exchange the authorization code for an Access Token:

```bash
curl -X POST -H "Content-Type: application/json" \
     -d '{"url":"https://listen.tidal.com/login/auth?code=abc123def456ghi789&lang=en"}' \
     http://<YOUR-SPOTIFLAC-IP>:6890/api/auth/tidal/callback
```

If successful, SpotiFLAC will respond with:
```json
{"success": true}
```

The premium token is now securely saved in your config directory (`tidal_token.json`). SpotiFLAC will handle all future token refreshes automatically in the background!

---

## Fallback Mechanism (Zero-Account Mode)

SpotiFLAC is designed to be highly resilient. If you choose **not** to authenticate (or if your token expires and cannot be refreshed), the download queue will **not** block.

Instead, SpotiFLAC will seamlessly bypass the official API and automatically hunt for the track across a distributed network of public community HiFi APIs (such as `squid.wtf`, `monochrome.tf`, and `qqdl.site`).