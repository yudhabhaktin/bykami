import type { ScreenProps } from "../App";
import { Doodle } from "../Doodle";
import { SheetPreview } from "../SheetPreview";

/**
 * Pick the frame, before the camera opens.
 *
 * Chosen here and only here. The frame decides how many photographs the session
 * needs, so a customer who learns that after shooting has been told too late —
 * and review used to offer the layout a second time, which meant they could
 * pick a four-cell design having taken three frames. The session names a
 * default, so tapping straight through lands on something.
 *
 * After the cut choice, because cut is a property of the paper and this is a
 * property of the picture: what comes out of the machine is settled first, then
 * what is printed on it.
 *
 * The preview is the same component the review screen uses, with no photos in
 * it — empty numbered cells are exactly what "this frame holds four" looks like.
 * Nothing is committed to the server here: the template travels with the print
 * request, so it is still only a choice until a sheet is queued.
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
        <div className="page-head">
          <Doodle shape="rainbow" className="page-doodle green" />
          <h1>Pilih frame</h1>
          <p className="muted">
            {template
              ? `${template.name} — butuh ${template.cells.length} foto.`
              : "Pilih tata letak cetakanmu."}
          </p>
        </div>

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
