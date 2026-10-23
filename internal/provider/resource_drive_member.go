// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package googleworkspace

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"

	"google.golang.org/api/drive/v3"
)

// A shared drive member is one Permission on the drive itself (the Drive API's
// `permissions` resource with fileId = the drive id), which is why this
// resource speaks the Drive API rather than the Admin SDK: shared-drive
// membership is a Drive concept, with its own roles (organizer, fileOrganizer,
// writer, commenter, reader) that the Directory API cannot express.
//
// The caller needs the `https://www.googleapis.com/auth/drive` scope and the
// Drive and Docs administrator privilege (a custom Workspace admin role
// carries it; a service account may hold that, like Groups). Every call
// passes `useDomainAdminAccess`, so the caller needs no membership of its
// own: a read-only role reads the membership, a full one reverts its drift,
// each exactly as the admin console's "Manage shared drives" page does. The
// privilege is service administration — settings and membership — never
// content: no files are read here.
func resourceDriveMember() *schema.Resource {
	return &schema.Resource{
		Description: "Drive Member resource manages the membership of a Google shared drive: one member " +
			"(a user, a group or a service account) holding one role. It resides under the " +
			"`https://www.googleapis.com/auth/drive` client scope, and the caller must hold the Drive and " +
			"Docs administrator privilege: every call passes useDomainAdminAccess, so no membership of " +
			"the drive itself is required.",

		CreateContext: resourceDriveMemberCreate,
		ReadContext:   resourceDriveMemberRead,
		UpdateContext: resourceDriveMemberUpdate,
		DeleteContext: resourceDriveMemberDelete,

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(5 * time.Minute),
			Update: schema.DefaultTimeout(5 * time.Minute),
		},

		Importer: &schema.ResourceImporter{
			StateContext: resourceDriveMemberImport,
		},

		Schema: map[string]*schema.Schema{
			"drive_id": {
				Description: "The id of the shared drive the member is added to. The drive itself is not " +
					"managed here: a service account cannot create one, so it is created by a person and " +
					"its id is an anchor.",
				Type:     schema.TypeString,
				ForceNew: true,
				Required: true,
			},
			"email": {
				Description: "The member's email address: a user (a service account is a user here), or a " +
					"Google Group.",
				Type:     schema.TypeString,
				ForceNew: true,
				Required: true,
			},
			"type": {
				Description: "The kind of principal the email names. A service account is a `user`. " +
					"Acceptable values are:" +
					"\n\t- `group`: The member is a Google Group." +
					"\n\t- `user`: The member is a user or a service account.",
				Type:             schema.TypeString,
				ForceNew:         true,
				Required:         true,
				ValidateDiagFunc: validation.ToDiagFunc(validation.StringInSlice([]string{"group", "user"}, false)),
			},
			"role": {
				Description: "The member's role on the shared drive, as the Drive API names it. " +
					"`organizer` is the console's Manager (full control of membership and settings), " +
					"`fileOrganizer` is Content manager, `writer` is Contributor. " +
					"Acceptable values are:" +
					"\n\t- `organizer`" +
					"\n\t- `fileOrganizer`" +
					"\n\t- `writer`" +
					"\n\t- `commenter`" +
					"\n\t- `reader`",
				Type:     schema.TypeString,
				Required: true,
				ValidateDiagFunc: validation.ToDiagFunc(validation.StringInSlice([]string{
					"organizer", "fileOrganizer", "writer", "commenter", "reader"}, false)),
			},
			"member_id": {
				Description: "The id of the permission backing this membership.",
				Type:        schema.TypeString,
				Computed:    true,
			},
			// Adding a computed id simply to override the `optional` id that gets added in the SDK
			// that will then display improperly in the docs
			"id": {
				Description: "The ID of this resource.",
				Type:        schema.TypeString,
				Computed:    true,
			},
		},
	}
}

func resourceDriveMemberCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*apiClient)

	driveId := d.Get("drive_id").(string)
	email := d.Get("email").(string)
	memberType := d.Get("type").(string)
	role := d.Get("role").(string)
	log.Printf("[DEBUG] Creating Drive Member %q in drive %s", email, driveId)

	driveService, diags := client.NewDriveService()
	if diags.HasError() {
		return diags
	}

	permissionObj := drive.Permission{
		Role:         role,
		Type:         memberType,
		EmailAddress: email,
	}

	permission, err := driveService.Permissions.
		Create(driveId, &permissionObj).
		SupportsAllDrives(true).
		UseDomainAdminAccess(true).
		SendNotificationEmail(false).
		Do()
	if err != nil {
		return diag.FromErr(fmt.Errorf("creating Drive Member %q in drive %s: %w", email, driveId, err))
	}

	d.Set("member_id", permission.Id)
	d.SetId(fmt.Sprintf("drives/%s/permissions/%s", driveId, permission.Id))

	log.Printf("[DEBUG] Finished creating Drive Member %q (%s)", email, permission.Id)

	return resourceDriveMemberRead(ctx, d, meta)
}

func resourceDriveMemberRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*apiClient)

	driveId := d.Get("drive_id").(string)
	permissionId := d.Get("member_id").(string)
	log.Printf("[DEBUG] Reading Drive Member %s in drive %s", permissionId, driveId)

	driveService, diags := client.NewDriveService()
	if diags.HasError() {
		return diags
	}

	permission, err := driveService.Permissions.
		Get(driveId, permissionId).
		SupportsAllDrives(true).
		UseDomainAdminAccess(true).
		Do()
	if err != nil {
		return handleNotFoundError(err, d, fmt.Sprintf("Drive Member %s in drive %s", permissionId, driveId))
	}

	// A permission that no longer names the declared principal is not this
	// membership: recreate rather than silently follow whatever the id points
	// at now. A domain-admin read-back omits emailAddress entirely (measured
	// on #592: the create's response carries it, useDomainAdminAccess reads
	// do not), so the guard and the state both keep the declared address when
	// the API returns none.
	if email := d.Get("email").(string); email != "" && permission.EmailAddress != "" && permission.EmailAddress != email {
		return diag.FromErr(fmt.Errorf("Drive Member %s in drive %s is %q, not the declared %q",
			permissionId, driveId, permission.EmailAddress, email))
	}

	if permission.EmailAddress != "" {
		d.Set("email", permission.EmailAddress)
	}
	d.Set("role", permission.Role)
	d.Set("type", permission.Type)

	return nil
}

func resourceDriveMemberUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*apiClient)

	driveId := d.Get("drive_id").(string)
	permissionId := d.Get("member_id").(string)
	log.Printf("[DEBUG] Updating Drive Member %s in drive %s", permissionId, driveId)

	driveService, diags := client.NewDriveService()
	if diags.HasError() {
		return diags
	}

	if d.HasChange("role") {
		permissionObj := drive.Permission{
			Role: d.Get("role").(string),
		}

		if _, err := driveService.Permissions.
			Update(driveId, permissionId, &permissionObj).
			SupportsAllDrives(true).
			UseDomainAdminAccess(true).
			Do(); err != nil {
			return diag.FromErr(fmt.Errorf("updating Drive Member %s in drive %s: %w", permissionId, driveId, err))
		}
	}

	log.Printf("[DEBUG] Finished updating Drive Member %s", permissionId)

	return resourceDriveMemberRead(ctx, d, meta)
}

func resourceDriveMemberDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*apiClient)

	driveId := d.Get("drive_id").(string)
	permissionId := d.Get("member_id").(string)
	log.Printf("[DEBUG] Deleting Drive Member %s in drive %s", permissionId, driveId)

	driveService, diags := client.NewDriveService()
	if diags.HasError() {
		return diags
	}

	if err := driveService.Permissions.
		Delete(driveId, permissionId).
		SupportsAllDrives(true).
		UseDomainAdminAccess(true).
		Do(); err != nil && !isNotFound(err) {
		return diag.FromErr(fmt.Errorf("deleting Drive Member %s in drive %s: %w", permissionId, driveId, err))
	}

	log.Printf("[DEBUG] Finished deleting Drive Member %s", permissionId)

	return nil
}

func resourceDriveMemberImport(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	parts := strings.Split(d.Id(), "/")

	// id is of format "drives/<drive_id>/permissions/<permission_id>[/<email>]".
	// The email is what the declaration holds and the domain-admin read never
	// returns (measured on #592), so an import without it lands in state with
	// no email — and the declared one then plans a replace. Name it.
	if len(parts) != 4 && len(parts) != 5 {
		return nil, fmt.Errorf("Drive Member Id (%s) is not of the correct format (drives/<drive_id>/permissions/<permission_id>[/<email>])", d.Id())
	}

	d.Set("drive_id", parts[1])
	d.Set("member_id", parts[3])
	if len(parts) == 5 {
		d.Set("email", parts[4])
	}

	return []*schema.ResourceData{d}, nil
}
