#!/usr/bin/env python3
import argparse
import json
import pathlib
import secrets


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--client-id", required=True)
    parser.add_argument("--bot-id", required=True, type=int)
    parser.add_argument("--ws-url", required=True)
    parser.add_argument("--secret")
    parser.add_argument("--work-dir", default=str(pathlib.Path.cwd()))
    return parser.parse_args()


def toml_string(value):
    return json.dumps(value, ensure_ascii=False)



def main():
    args = parse_args()
    secret = args.secret or secrets.token_hex(16)
    lion_value = json.dumps({args.client_id: secret}, ensure_ascii=False)

    print(f"Lion key: ugc.agent.tools.bridge.clients")
    print(f"Lion value: {lion_value}")
    print()
    print("cc-connect config:")
    print("[[projects]]")
    print('name = "daxiang-remote"')
    print()
    print("[projects.agent]")
    print('type = "claudecode"')
    print()
    print("[projects.agent.options]")
    print(f"work_dir = {toml_string(args.work_dir)}")
    print('mode = "default"')
    print()
    print("[[projects.platforms]]")
    print('type = "daxiangbridge"')
    print()
    print("[projects.platforms.options]")
    print(f"ws_url = {toml_string(args.ws_url)}")
    print(f"client_id = {toml_string(args.client_id)}")
    print(f"client_secret = {toml_string(secret)}")
    print(f"bot_id = {args.bot_id}")
    print('ping_interval = "30s"')
    print('reconnect_min_backoff = "3s"')
    print('reconnect_max_backoff = "60s"')


if __name__ == "__main__":
    main()
