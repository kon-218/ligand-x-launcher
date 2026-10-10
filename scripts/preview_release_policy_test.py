import unittest
from preview_release_policy import validate_selection


class PreviewReleasePolicyTest(unittest.TestCase):
    def test_preview_exposes_exact_opt_in_without_latest(self):
        self.assertEqual(validate_selection('preview', 'v0.1.4-preview.1', 'proto-mutation', False, ''), 'proto-mutation')

    def test_stable_stays_without_preview(self):
        self.assertEqual(validate_selection('stable', 'v0.1.4', 'none', True, ''), '')

    def test_preview_cannot_reuse_stable_binary_or_be_recommended(self):
        for version, bundle, latest, reuse in [
            ('v0.1.4', 'proto-mutation', False, ''),
            ('v0.1.4-preview.1', 'none', False, ''),
            ('v0.1.4-preview.1', 'bioemu', False, ''),
            ('v0.1.4-preview.1', 'proto-mutation', True, ''),
            ('v0.1.4-preview.1', 'proto-mutation', False, 'v0.1.3'),
        ]:
            with self.assertRaises(ValueError):
                validate_selection('preview', version, bundle, latest, reuse)
        with self.assertRaises(ValueError):
            validate_selection('stable', 'v0.1.4', 'proto-mutation', False, '')


if __name__ == '__main__':
    unittest.main()
