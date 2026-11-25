import { useEffect, useState } from "react";
import { hosts as hostsApi } from "@/api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";
import type { Host, HostInvite } from "@/gen/inaba/v1/server_pb";
import { errorText, showError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/** An invite link to a host, opened: what it leads to and grants, and joining it. */
export function InviteDialog({ token, hosts, onJoined, onClose }: { token?: string; hosts?: Host[]; onJoined: (host: Host) => void; onClose: () => void }) {
  const { t } = useI18n();
  const [invite, setInvite] = useState<HostInvite>();
  const [error, setError] = useState("");
  const [joining, setJoining] = useState(false);
  useEffect(() => {
    setInvite(undefined);
    setError("");
    if (token) hostsApi.getHostInvite({ token }).then(setInvite, (e) => setError(errorText(e)));
  }, [token]);
  // Accepting only ever raises one's role, so for someone already in it matters only when a
  // member opens a manager's invite; with the host not listed yet, it reads as access they have.
  const raises = invite?.joined && invite.role === "manager" && hosts?.find((h) => h.id === invite.hostId)?.role === "member";
  const join = () => {
    setJoining(true);
    hostsApi
      .acceptHostInvite({ token: token! })
      .then(onJoined, showError)
      .finally(() => setJoining(false));
  };
  return (
    <Dialog open={token !== undefined} onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{invite ? t("invite.title", { host: invite.hostName }) : t("invite.loading")}</DialogTitle>
          <DialogDescription>{error || (invite ? t(raises ? "invite.raise" : invite.joined ? "invite.joined" : invite.ownerName ? "invite.help" : "invite.helpShared", { owner: invite.ownerName }) : "")}</DialogDescription>
        </DialogHeader>
        {invite && !invite.joined && (
          <p className="text-sm">
            {t(invite.role === "manager" ? "invite.roleManager" : "invite.roleMember")}
          </p>
        )}
        {!invite && !error && <Spinner />}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("device.cancel")}
          </Button>
          {invite && (
            <Button disabled={joining} onClick={join}>
              {joining && <Spinner data-icon="inline-start" />}
              {t(raises ? "invite.becomeManager" : invite.joined ? "invite.open" : "invite.join")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
