#!/usr/bin/env python3
import argparse
import json
import sys
import time
import urllib.request
import webbrowser


def main():
    parser = argparse.ArgumentParser(
        description="Authenticate SpotiFLAC with Tidal using Device Code Flow."
    )
    parser.add_argument(
        "--host",
        default="http://localhost:6890",
        help="The URL of your SpotiFLAC instance (e.g. http://192.168.1.10:6890)",
    )
    parser.add_argument(
        "--token",
        default=None,
        help="Your SpotiFLAC JWT token or API key (sk_spotiflac_...)",
    )
    args = parser.parse_args()

    host = args.host.rstrip("/")

    print("==========================================")
    print("  SpotiFLAC - Tidal Authentication        ")
    print("==========================================")
    print(f"Connecting to SpotiFLAC at: {host}")

    headers = {}
    if args.token:
        token = args.token.strip()
        if token.startswith("sk_spotiflac_"):
            headers["X-API-Key"] = token
        else:
            headers["Authorization"] = f"Bearer {token}"

    # 1. Start device auth flow
    print("\n[1/3] Starting Tidal authentication...")
    headers_with_ct = {**headers, "Content-Type": "application/json"}
    req = urllib.request.Request(
        f"{host}/api/v1/auth/tidal/device/start",
        data=b"{}",
        headers=headers_with_ct,
        method="POST",
    )
    try:
        with urllib.request.urlopen(req) as response:
            data = json.loads(response.read().decode())
    except urllib.error.HTTPError as e:
        print(f"❌ Error: SpotiFLAC returned HTTP {e.code}.")
        if e.code in [401, 403]:
            print("Hint: Provide a valid token with --token=YOUR_JWT_OR_API_KEY")
        sys.exit(1)
    except Exception as e:
        print(f"❌ Error: Could not connect to SpotiFLAC at {host}")
        print(f"Details: {e}")
        sys.exit(1)

    device_code = data.get("device_code")
    verification_uri = data.get("verification_uri_complete") or data.get("verification_uri", "")
    user_code = data.get("user_code", "")
    interval = max(data.get("interval", 5), 5)
    expires_in = data.get("expires_in", 300)

    if not device_code or not verification_uri:
        print("❌ Error: Invalid response from server.")
        sys.exit(1)

    # 2. Ask user to authorize
    print("\n------------------------------------------------------------------")
    print("🚨 ACTION REQUIRED 🚨")
    print(f"Open this link in your browser and log in with your Tidal Premium account:")
    print(f"\n  {verification_uri}\n")
    if user_code:
        print(f"If asked for a code, enter: {user_code}")
    print("------------------------------------------------------------------\n")

    try:
        webbrowser.open(verification_uri)
    except Exception:
        pass

    # 3. Poll until authorized or expired
    print(f"[2/3] Waiting for authorization (checking every {interval}s)...")
    deadline = time.time() + expires_in
    while time.time() < deadline:
        time.sleep(interval)
        poll_req = urllib.request.Request(
            f"{host}/api/v1/auth/tidal/device/poll",
            data=json.dumps({"device_code": device_code}).encode(),
            headers=headers_with_ct,
            method="POST",
        )
        try:
            with urllib.request.urlopen(poll_req) as response:
                result = json.loads(response.read().decode())
        except Exception:
            continue

        status = result.get("status")
        if status == "authorized":
            print("\n[3/3] ✅ SUCCESS! Tidal account connected.")
            print("SpotiFLAC can now download Lossless FLACs directly from the official Tidal API.")
            return
        elif status == "expired":
            print("\n❌ Authorization expired. Please run the script again.")
            sys.exit(1)
        elif status == "denied":
            print("\n❌ Authorization denied by user.")
            sys.exit(1)
        elif status == "error":
            print(f"\n❌ Error: {result.get('error', 'unknown')}")
            sys.exit(1)
        else:
            print("  Still waiting...", end="\r")

    print("\n❌ Timed out waiting for authorization.")
    sys.exit(1)


if __name__ == "__main__":
    main()
