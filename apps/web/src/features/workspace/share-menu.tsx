import { Button } from "@skillsignal/ui/components/button";
import {
	Popover,
	PopoverContent,
	PopoverTrigger,
} from "@skillsignal/ui/components/popover";
import {
	ToggleGroup,
	ToggleGroupItem,
} from "@skillsignal/ui/components/toggle-group";
import { ChevronDown, Copy, Link as LinkIcon } from "lucide-react";
import type { JobDetail } from "./client";
import { applicationStatus, type Invitation } from "./shared";

const VALIDITY_OPTIONS = [
	{ hours: 24, label: "24 hours" },
	{ hours: 168, label: "7 days" },
	{ hours: 720, label: "30 days" },
];

type ShareMenuProps = {
	job: JobDetail;
	invitation: Invitation | null;
	inviteHours: number;
	copied: boolean;
	isCreating: boolean;
	setInviteHours: (hours: number) => void;
	onCreateInvitation: () => void;
	onCopyLink: () => void;
	onEditWindow: () => void;
};

export const ShareMenu = ({
	job,
	invitation,
	inviteHours,
	copied,
	isCreating,
	setInviteHours,
	onCreateInvitation,
	onCopyLink,
	onEditWindow,
}: ShareMenuProps) => (
	<Popover>
		<PopoverTrigger asChild>
			<Button size="sm" variant="outline">
				Share
				<ChevronDown />
			</Button>
		</PopoverTrigger>
		<PopoverContent align="end" className="share-menu">
			<section aria-labelledby="share-invite-title">
				<h3 id="share-invite-title">Invite a candidate</h3>
				{invitation ? (
					<div className="share-invitation">
						<div className="share-link-row">
							<code>{`${window.location.origin}/apply/${invitation.token}`}</code>
							<Button
								onClick={onCopyLink}
								size="sm"
								variant="outline"
							>
								<Copy />
								{copied ? "Copied" : "Copy"}
							</Button>
						</div>
						<p>
							Passcode{" "}
							<code className="passcode">
								{invitation.passcode}
							</code>
						</p>
						<p className="muted-copy">
							Single use. Expires{" "}
							{new Date(invitation.expiresAt).toLocaleString()}.
						</p>
					</div>
				) : (
					<p className="muted-copy">
						Each link and passcode works for one submission.
					</p>
				)}
				<div className="share-validity">
					<span className="muted-copy">Valid for</span>
					<ToggleGroup
						aria-label="Invitation validity"
						onValueChange={(value) => {
							if (value) setInviteHours(Number(value));
						}}
						size="sm"
						type="single"
						value={String(inviteHours)}
						variant="outline"
					>
						{VALIDITY_OPTIONS.map((option) => (
							<ToggleGroupItem
								key={option.hours}
								value={String(option.hours)}
							>
								{option.label}
							</ToggleGroupItem>
						))}
					</ToggleGroup>
				</div>
				<Button
					className="w-full"
					disabled={isCreating}
					onClick={onCreateInvitation}
					size="sm"
				>
					<LinkIcon />
					{invitation
						? "Create another invitation"
						: "Create invitation"}
				</Button>
			</section>
			<section
				aria-labelledby="share-window-title"
				className="share-window"
			>
				<div>
					<h3 id="share-window-title">Application window</h3>
					<p>{applicationStatus(job)}</p>
				</div>
				<Button onClick={onEditWindow} size="sm" variant="outline">
					Edit
				</Button>
			</section>
		</PopoverContent>
	</Popover>
);
