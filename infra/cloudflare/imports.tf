// The four Pages projects were created through the Cloudflare API before a
// Terraform-usable token existed, so that deploys could start immediately.
//
// These blocks adopt them into state rather than recreating them. Run once:
//
//   terraform plan    # expect: 4 to import, 0 to add, 0 to change, 0 to destroy
//   terraform apply
//
// A plan proposing to *create* any of these means the token cannot see the
// existing projects — check its account scope before applying, or it will fail
// on a name collision.
//
// Once imported, delete this file. Import blocks are a one-time migration, not
// permanent configuration.

import {
  to = cloudflare_pages_project.site["root"]
  id = "7f0b5483fb3d4bd4edadca7a94e96e5f/bykami-root"
}

import {
  to = cloudflare_pages_project.site["studio"]
  id = "7f0b5483fb3d4bd4edadca7a94e96e5f/bykami-studio"
}

import {
  to = cloudflare_pages_project.site["booth"]
  id = "7f0b5483fb3d4bd4edadca7a94e96e5f/bykami-booth"
}

import {
  to = cloudflare_pages_project.site["dimsamcong"]
  id = "7f0b5483fb3d4bd4edadca7a94e96e5f/bykami-dimsamcong"
}
