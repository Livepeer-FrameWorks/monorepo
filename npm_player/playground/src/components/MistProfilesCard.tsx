import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import type { MistProfile } from "@/lib/types";

export type MistProfilesCardProps = {
  profiles: MistProfile[];
  selectedProfileId: string | null;
  onSelectProfile: (id: string | null) => void;
  onSaveProfile: () => void;
  onUpdateProfile: () => void;
  onDeleteProfile: () => void;
  onNewProfile: () => void;
};

export function MistProfilesCard({
  profiles,
  selectedProfileId,
  onSelectProfile,
  onSaveProfile,
  onUpdateProfile,
  onDeleteProfile,
  onNewProfile
}: MistProfilesCardProps) {
  const hasSelectedProfile = selectedProfileId !== null;

  return (
    <div className="slab">
      <div className="slab-header">
        <h3 className="slab-title">Saved Mist Profiles</h3>
        <p className="slab-description">Capture frequently used nodes (local Docker, staging, etc.) and switch between them instantly.</p>
      </div>
      <div className="slab-body--flush">
        <div className="slab-form-group">
          <Label htmlFor="mist-profile-select">Active profile</Label>
          <select
            id="mist-profile-select"
            className="mt-2 h-10 w-full border border-input bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
            value={selectedProfileId ?? ""}
            onChange={(event) => onSelectProfile(event.target.value ? event.target.value : null)}
          >
            <option value="">Unsaved workspace</option>
            {profiles.map((profile) => (
              <option key={profile.id} value={profile.id}>
                {profile.name}
              </option>
            ))}
          </select>
        </div>
      </div>
      <div className="slab-actions slab-actions--row">
        <Button variant="ghost" onClick={onSaveProfile}>
          Save as new
        </Button>
        <Button variant="ghost" onClick={onUpdateProfile} disabled={!hasSelectedProfile}>
          Update
        </Button>
        <Button variant="ghost" onClick={onDeleteProfile} disabled={!hasSelectedProfile}>
          Delete
        </Button>
        <Button variant="ghost" onClick={onNewProfile}>
          Reset
        </Button>
      </div>
    </div>
  );
}
