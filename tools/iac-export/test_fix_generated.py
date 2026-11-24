import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
FIX = os.path.join(HERE, "fix_generated.py")

SAMPLE = '''# __generated__ by OpenTofu
resource "google_compute_firewall" "fw" {
  destination_ranges = []
  name               = "fw"
  source_ranges      = ["1.2.3.4/32"]
  target_tags        = []
}
# __generated__ by OpenTofu
resource "google_pubsub_subscription" "s" {
  name = "s"
  expiration_policy {
    ttl = ""
  }
}
# __generated__ by OpenTofu
resource "google_secret_manager_secret" "sec" {
  secret_id = "sec"
  replication {
    auto {
    }
  }
}
# __generated__ by OpenTofu
resource "google_storage_bucket" "b" {
  name = "b"
  labels = {
    keep = ""
  }
  logging {
    log_bucket = ""
  }
}
# __generated__ by OpenTofu
resource "google_cloud_run_v2_service" "svc" {
  name = "svc"
  template {
    containers {
      env {
        name  = "DATABASE_URL"
        value = "postgresql+psycopg://app:hunter2@10.0.0.1:5432/db"
      }
      env {
        name  = "LOG_LEVEL"
        value = "info"
      }
      env {
        name  = "STRIPE_API_KEY"
        value = "not-a-pattern-but-named-like-one"
      }
    }
  }
}
# __generated__ by OpenTofu
resource "google_project_service" "api" {
  disable_on_destroy = null
  service            = "run.googleapis.com"
}
'''


def run(text):
    with tempfile.NamedTemporaryFile("w", suffix=".tf", delete=False) as f:
        f.write(text)
        path = f.name
    p = subprocess.run([sys.executable, FIX, path], capture_output=True, text=True)
    out = open(path).read()
    os.unlink(path)
    return p.returncode, out, p.stderr


class FixGenerated(unittest.TestCase):
    def setUp(self):
        self.code, self.out, self.err = run(SAMPLE)

    def test_exit_ok(self):
        self.assertEqual(self.code, 0, self.err)

    def test_drops_empty_lists(self):
        self.assertNotIn("destination_ranges", self.out)
        self.assertNotIn("target_tags", self.out)
        self.assertIn('source_ranges      = ["1.2.3.4/32"]', self.out)

    def test_keeps_meaningful_empty_values(self):
        self.assertIn('ttl = ""', self.out)            # Pub/Sub: never expire
        self.assertIn("auto {}", self.out)              # secret replication
        self.assertIn('keep = ""', self.out)            # values inside label maps

    def test_drops_blocks_emptied_by_cleanup(self):
        self.assertNotIn("logging", self.out)

    def test_redacts_credentials_and_keeps_plain_config(self):
        self.assertNotIn("hunter2", self.out)
        self.assertNotIn("not-a-pattern-but-named-like-one", self.out)
        self.assertIn('value = "info"', self.out)
        self.assertEqual(self.out.count("REDACTED"), 2)

    def test_cloud_run_template_owned_by_app_ci(self):
        self.assertIn("ignore_changes = [client, client_version, template", self.out)

    def test_never_disable_apis(self):
        self.assertIn("disable_on_destroy = false", self.out)

    def test_fails_when_credentials_remain(self):
        leak = 'resource "google_cloudfunctions_function" "f" {\n  description = "-----BEGIN PRIVATE KEY----- abc"\n}\n'
        code, _, err = run("# __generated__ by OpenTofu\n" + leak)
        self.assertEqual(code, 2)
        self.assertIn("FAIL", err)


if __name__ == "__main__":
    unittest.main()
