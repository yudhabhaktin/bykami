import type { ScreenProps } from "../App";
import { SheetPreview } from "../SheetPreview";

/**
 * Pick the frame, before the camera opens.
 *
 * Chosen here rather than only at review because the frame decides how many
 * photos the session needs, and a customer who learns that after shooting has
 * been told too late. The package still names a default, so tapping straight
 * through gets the frame the price list advertised.
 *
 * The preview is the same component the review screen uses, with no photos in
 * it — empty numbered cells are exactly what "this frame holds four" looks like.
 * Nothing is committed to the server here: the template travels with the print
 * request, so changing your mind at review is still free.
 */
export function Frame({
  state,
  setStep,
  templateId,
  setTemplateId,
}: ScreenProps & { templateId: string; setTemplateId: (id: string) => void }) {
  const template = state.templates.find((t) => t.id === templateId);

  return (
    <div className="frame grow">
      <div className="preview">
        {template && <SheetPreview template={template} chosen={[]} />}
      </div>

      <div className="picker">
        <h1>Pilih frame</h1>
        <p className="muted">
          {template
            ? `${template.name} — butuh ${template.cells.length} foto.`
            : "Pilih tata letak cetakanmu."}
        </p>

        <div className="frames">
          {state.templates.map((t) => (
            <button
              key={t.id}
              className="frame-card"
              aria-pressed={t.id === templateId}
              onClick={() => setTemplateId(t.id)}
            >
              <SheetPreview template={t} chosen={[]} />
              <span className="frame-name">{t.name}</span>
              <span className="muted small">{t.cells.length} foto</span>
            </button>
          ))}
        </div>

        <div className="actions">
          <button className="btn big" onClick={() => setStep("capture")} disabled={!template}>
            Lanjut
          </button>
        </div>
      </div>
    </div>
  );
}
