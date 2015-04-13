#!/bin/sh
#
# Copyright (c) 2014 Jeromy Johnson
# MIT Licensed; see the LICENSE file in this repository.
#

test_description="Test ipfs repo operations"

. lib/test-lib.sh

test_init_ipfs

# test publishing a hash

test_expect_success "'ipfs name publish' succeeds" '
	PEERID=`ipfs id -format="<id>"` &&
	ipfs name publish "$HASH_WELCOME_DOCS" >publish_out
'

test_expect_success "publish output looks good" '
	echo "Published name $PEERID to $HASH_WELCOME_DOCS" >expected1 &&
	test_cmp publish_out expected1
'

test_expect_success "'ipfs name resolve' succeeds" '
	ipfs name resolve "$PEERID" >output
'

test_expect_success "resolve output looks good" '
	printf "%s" "$HASH_WELCOME_DOCS" >expected2 &&
	test_cmp output expected2
'

# now test with a path

test_expect_success "'ipfs name publish' succeeds" '
	PEERID=`ipfs id -format="<id>"` &&
	ipfs name publish "/ipfs/$HASH_WELCOME_DOCS/help" >publish_out
'

test_expect_success "publish a path looks good" '
	echo "Published name $PEERID to $HASH_HELP_PAGE" >expected3 &&
	test_cmp publish_out expected3
'

test_expect_success "'ipfs name resolve' succeeds" '
	ipfs name resolve "$PEERID" >output
'

test_expect_success "resolve output looks good" '
	printf "%s" "$HASH_HELP_PAGE" >expected4 &&
	test_cmp output expected4
'

test_done
