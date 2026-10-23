// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package googleworkspace

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// The test fixture is external to the code: a shared drive the caller holds
// Manager on (GOOGLEWORKSPACE_ACC_TEST_DRIVE_ID), because no service account
// can create one. Every principal the test adds is a throwaway it removes
// itself; the drive itself is left as found.
func TestAccResourceDriveMember(t *testing.T) {
	t.Parallel()

	driveId, memberEmail := accPreCheckWithTestDrive(t)

	testAccCheckDriveMemberDestroy := func(state *terraform.State) error {
		for _, rs := range state.RootModule().Resources {
			if rs.Type != "googleworkspace_drive_member" {
				continue
			}
			client, err := googleworkspaceTestClient()
			if err != nil {
				return err
			}
			driveService, diags := client.NewDriveService()
			if diags.HasError() {
				return fmt.Errorf("creating Drive service: %v", diags)
			}
			_, err = driveService.Permissions.
				Get(rs.Primary.Attributes["drive_id"], rs.Primary.Attributes["member_id"]).
				SupportsAllDrives(true).
				Do()
			if err == nil {
				return fmt.Errorf("Drive Member %s still present in drive %s",
					rs.Primary.Attributes["member_id"], rs.Primary.Attributes["drive_id"])
			}
			if !isNotFound(err) {
				return err
			}
		}
		return nil
	}

	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: providerFactories,
		CheckDestroy:      testAccCheckDriveMemberDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccDriveMember(driveId, memberEmail, "writer"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("googleworkspace_drive_member.test", "drive_id", driveId),
					resource.TestCheckResourceAttr("googleworkspace_drive_member.test", "email", memberEmail),
					resource.TestCheckResourceAttr("googleworkspace_drive_member.test", "role", "writer"),
					resource.TestCheckResourceAttr("googleworkspace_drive_member.test", "type", "user"),
				),
			},
			{
				ResourceName:      "googleworkspace_drive_member.test",
				ImportState:       true,
				ImportStateVerify: true,
				// email, role and type all arrive from the read-back.
			},
			{
				Config: testAccDriveMember(driveId, memberEmail, "fileOrganizer"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("googleworkspace_drive_member.test", "role", "fileOrganizer"),
				),
			},
		},
	})
}

func testAccDriveMember(driveId, memberEmail, role string) string {
	return fmt.Sprintf(`
resource "googleworkspace_drive_member" "test" {
  drive_id = %q
  email    = %q
  type     = "user"
  role     = %q
}
`, driveId, memberEmail, role)
}

// accPreCheckWithTestDrive skips the acceptance test unless the fixture drive
// and member are named. The member the test adds is removed by the destroy
// step, so nothing lingers on the drive.
func accPreCheckWithTestDrive(t *testing.T) (string, string) {
	t.Helper()

	// The credential pre-check lives in the TestCase's PreCheck, which the
	// SDK harness runs only under TF_ACC; nothing here may demand
	// credentials of a plain `go test`.
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test; set TF_ACC and the GOOGLEWORKSPACE_* fixtures to run it")
	}

	testAccPreCheck(t)

	driveId := os.Getenv("GOOGLEWORKSPACE_ACC_TEST_DRIVE_ID")
	memberEmail := os.Getenv("GOOGLEWORKSPACE_ACC_TEST_MEMBER_EMAIL")
	if driveId == "" || memberEmail == "" {
		t.Skip("GOOGLEWORKSPACE_ACC_TEST_DRIVE_ID and GOOGLEWORKSPACE_ACC_TEST_MEMBER_EMAIL must be set; acceptance tests need a shared drive the caller manages and a member email")
	}

	return driveId, memberEmail
}
