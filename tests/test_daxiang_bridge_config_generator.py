import json
import pathlib
import subprocess
import sys
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "generate_daxiang_bridge_config.py"


class DaxiangBridgeConfigGeneratorTest(unittest.TestCase):
    def test_generates_lion_json_and_cc_connect_snippet(self):
        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--client-id",
                "test-ccconnect-01",
                "--bot-id",
                "10001",
                "--ws-url",
                "ws://10.205.139.150:8080/ws",
                "--secret",
                "0123456789abcdef0123456789abcdef",
                "--work-dir",
                "/tmp/custom-cc-connect",
            ],
            capture_output=True,
            text=True,
            check=True,
        )

        output = result.stdout
        self.assertIn("ugc.agent.tools.bridge.clients", output)
        self.assertIn('"test-ccconnect-01": "0123456789abcdef0123456789abcdef"', output)
        self.assertIn('ws_url = "ws://10.205.139.150:8080/ws"', output)
        self.assertIn('client_id = "test-ccconnect-01"', output)
        self.assertIn('client_secret = "0123456789abcdef0123456789abcdef"', output)
        self.assertIn('work_dir = "/tmp/custom-cc-connect"', output)
        self.assertIn("bot_id = 10001", output)

    def test_generates_random_secret_when_not_provided(self):
        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--client-id",
                "test-ccconnect-02",
                "--bot-id",
                "10002",
                "--ws-url",
                "ws://10.205.139.150:8080/ws",
            ],
            capture_output=True,
            text=True,
            check=True,
        )

        output = result.stdout.splitlines()
        lion_line = next(line for line in output if line.startswith("Lion value: "))
        lion_value = json.loads(lion_line.removeprefix("Lion value: "))
        secret = lion_value["test-ccconnect-02"]

        self.assertRegex(secret, r"^[0-9a-f]{32}$")
        self.assertIn(f'client_secret = "{secret}"', result.stdout)

    def test_escapes_toml_string_values(self):
        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--client-id",
                'test-ccconnect-\\"01',
                "--bot-id",
                "10001",
                "--ws-url",
                'ws://10.205.139.150:8080/ws?note=\\"quoted\\"',
                "--secret",
                'abc\\"def0123456789abcdef0123456789',
                "--work-dir",
                '/tmp/cc-connect/\\"quoted\\"',
            ],
            capture_output=True,
            text=True,
            check=True,
        )

        escaped_work_dir = json.dumps('/tmp/cc-connect/\\"quoted\\"', ensure_ascii=False)
        escaped_ws_url = json.dumps('ws://10.205.139.150:8080/ws?note=\\"quoted\\"', ensure_ascii=False)
        escaped_client_id = json.dumps('test-ccconnect-\\"01', ensure_ascii=False)
        escaped_secret = json.dumps('abc\\"def0123456789abcdef0123456789', ensure_ascii=False)

        self.assertIn(f'work_dir = {escaped_work_dir}', result.stdout)
        self.assertIn(f'ws_url = {escaped_ws_url}', result.stdout)
        self.assertIn(f'client_id = {escaped_client_id}', result.stdout)
        self.assertIn(f'client_secret = {escaped_secret}', result.stdout)

        output = result.stdout.splitlines()
        lion_line = next(line for line in output if line.startswith("Lion value: "))
        lion_value = json.loads(lion_line.removeprefix("Lion value: "))
        self.assertEqual(lion_value['test-ccconnect-\\"01'], 'abc\\"def0123456789abcdef0123456789')


if __name__ == "__main__":
    unittest.main()
