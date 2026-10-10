"""Validate tester release selection before packaging any launcher artifact."""
import argparse
import re


def validate_selection(channel, version, bundle, promote_latest, reuse_from):
    if channel == 'stable':
        if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+', version) or bundle != 'none':
            raise ValueError('stable packaging requires vX.Y.Z and no preview bundle')
        return ''
    if channel != 'preview' or not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+-preview\.[1-9][0-9]*', version):
        raise ValueError('tester packaging requires vX.Y.Z-preview.N')
    if bundle != 'proto-mutation' or promote_latest or reuse_from:
        raise ValueError('Proto preview requires its own launcher build and cannot move latest')
    return bundle


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--channel', required=True)
    parser.add_argument('--version', required=True)
    parser.add_argument('--bundle', default='none')
    parser.add_argument('--promote-latest', choices=['true', 'false'], required=True)
    parser.add_argument('--reuse-from', default='')
    args = parser.parse_args()
    try:
        selected = validate_selection(args.channel, args.version, args.bundle, args.promote_latest == 'true', args.reuse_from)
    except ValueError as error:
        parser.error(str(error))
    print('preview_bundles=' + selected)


if __name__ == '__main__':
    main()
