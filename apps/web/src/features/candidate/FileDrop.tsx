import { Button } from "@skillsignal/ui/components/button";
import { FileText, UploadCloud, X } from "lucide-react";
import { type DragEvent, useState } from "react";

type FileDropProps = {
	id: string;
	accept: string;
	file: File | null;
	hint: string;
	prompt: string;
	onFile: (file: File | null) => void;
};

const formatSize = (bytes: number) =>
	bytes < 1024 * 1024
		? `${Math.max(1, Math.round(bytes / 1024))} KB`
		: `${(bytes / (1024 * 1024)).toFixed(1)} MB`;

export const FileDrop = ({
	id,
	accept,
	file,
	hint,
	prompt,
	onFile,
}: FileDropProps) => {
	const [isDragging, setIsDragging] = useState(false);
	const onDrop = (event: DragEvent<HTMLLabelElement>) => {
		event.preventDefault();
		setIsDragging(false);
		const dropped = event.dataTransfer.files[0];
		if (dropped) onFile(dropped);
	};
	const input = (
		<input
			accept={accept}
			className="sr-only"
			id={id}
			onChange={(event) => {
				onFile(event.currentTarget.files?.[0] ?? null);
				event.currentTarget.value = "";
			}}
			type="file"
		/>
	);

	if (file) {
		return (
			<div className="file-chosen">
				<FileText aria-hidden />
				<div className="file-chosen-text">
					<strong>{file.name}</strong>
					<span>{formatSize(file.size)}</span>
				</div>
				{input}
				<Button asChild size="sm" variant="outline">
					<label htmlFor={id}>Replace</label>
				</Button>
				<Button
					aria-label={`Remove ${file.name}`}
					onClick={() => onFile(null)}
					size="icon-sm"
					variant="ghost"
				>
					<X />
				</Button>
			</div>
		);
	}

	return (
		<label
			className="file-drop"
			data-dragging={isDragging || undefined}
			htmlFor={id}
			onDragLeave={() => setIsDragging(false)}
			onDragOver={(event) => {
				event.preventDefault();
				setIsDragging(true);
			}}
			onDrop={onDrop}
		>
			{input}
			<UploadCloud aria-hidden />
			<span>
				<strong>{prompt}</strong> or browse
			</span>
			<small>{hint}</small>
		</label>
	);
};
