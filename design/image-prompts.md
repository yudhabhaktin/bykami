# Image prompts — generated stand-ins

> **The gallery no longer needs these.** It holds six real sessions from the
> studio's own Instagram, and a real photograph beats the best prompt here every
> time. Kept for the two frames `assets-needed.md` still has open — the empty
> room and the printed strip — and for whenever something is needed before it
> can be shot.
>
> Read rule 1 and rule 3 even for a single frame. Rule 2 is why the first
> generated set had to be thrown away.

Written against the shot list in `assets-needed.md`; each frame there has a job,
and a picture that does not do that job is decoration however good it looks.

---

## Rule 1 — generate the room first, then put people in it

The current six read as six different buildings: a black void, a brutalist
corridor, a concrete stairwell, a cavernous industrial hall. There is no studio
in them, only a mood. A gallery whose job is "this is our room" cannot be six
rooms.

So the order is fixed:

1. Generate **frame 05, the empty room**, until you have one you would book.
2. Attach that image as a **reference / style image** on every one of 01–04, with
   a line in the prompt saying *"same studio room, same lighting kit, same
   backdrop as the reference image."*
3. Only then generate the people.

Most tools support this: ChatGPT and Gemini take an uploaded image alongside the
prompt, Midjourney takes `--cref` / `--sref` plus an image URL. If yours does
not, generate all five in a single conversation and refer back — *"same room as
the last one, different backdrop"* — which holds consistency about as well.

## Rule 2 — the people are Banyuwangi, not Milan

Every model in the current set reads as European, in tailoring and evening wear.
The studio is in Jajag, and the one real asset in the set — frame 06 — shows five
young Indonesian women, one in a hijab, pulling faces. Those two sets of pictures
are selling different businesses.

Every prompt below therefore specifies **Indonesian / Southeast Asian subjects,
late teens to early thirties**, and at least one frame should include a woman in
a hijab, because that is who is in the real strip and who is in Banyuwangi. Not
as a gesture — as accuracy. A customer who cannot find themselves in the grid
reads the studio as not for them.

Dress is casual: plain tees, oversized shirts, denim, cardigans, sneakers.
Monochrome keeps it looking composed without needing suits.

## Rule 3 — it has to look like self photo

No photographer in any frame. The product is the room and the remote in your
hand, and every prompt below either shows the remote or shows nobody holding a
camera. A frame with a photographer in it is a picture of a different service.

---

## House style — paste at the top of every prompt

> Black and white editorial photograph, monochrome, no colour anywhere. Deep
> rich blacks, clean controlled highlights, smooth mid-tones. Lit with studio
> strobes: a large softbox key, a subtle rim light for separation from the
> backdrop. Shot on a 35mm full-frame camera, 50mm lens, f/4, sharp focus, fine
> natural film grain. Real skin texture — pores, flyaway hair, slight
> imperfection — not retouched or airbrushed. Photojournalistic realism, not
> illustration, not 3D render. 3:2 landscape.

And the standing negative list:

> No text, no watermark, no logo, no signature. No colour. No photographer, no
> tripod-mounted camera pointed at the subject. No extra fingers, no distorted
> hands. Not glossy, not high-fashion, not airbrushed, not HDR.

**Settings:** aspect ratio **3:2 landscape**, the highest resolution offered —
minimum 1536 × 1024, ideally 2400 px on the long edge. The site downsamples to
1200 × 800, so headroom costs nothing and a crop stays possible.
Midjourney suffix: `--ar 3:2 --style raw --s 100`.

---

## 05 — the room, empty *(generate this one first)*

**Its job:** a first-timer is buying a room they have never seen. This frame is
the whole reason the gallery exists, and it is the one that gets checked against
reality the moment somebody walks in.

> [house style] A small, clean self-photo studio room, completely empty of
> people. A white seamless paper backdrop curves down the back wall and onto the
> floor in a smooth cyclorama sweep. One large octagonal softbox on a stand at
> camera left, angled down toward the backdrop; one smaller strobe with a
> reflector dish at camera right. A simple black cube stool sits on the sweep,
> slightly off-centre. Plain grey painted side walls, a modest ceiling. In the
> bottom right foreground, slightly out of focus, a hand holds a small black
> remote shutter toward the room. Wide shot showing the whole set, taken from
> where a customer would stand. Warm even light, welcoming rather than dramatic.

**Must be true:** small and domestic-scale. **Do not** let it generate a
warehouse — the current version is a vast industrial hall with exposed concrete
beams, which promises a room this business does not have. Add to the negatives:
*no warehouse, no industrial hall, no exposed concrete ceiling beams, no large
empty floor space.*

**Honestly:** this is the one frame that should not be generated at all. It is a
photograph of the premises, it takes ten minutes and a phone on a chair, and the
address on this page is real. Generate it as a stopgap; replace it first.

---

## 01 — solo portrait, the anchor

**Its job:** carry the grid. It is the first thing anyone sees, and it sets
whether the page reads as a studio or as a stock library.

> [house style] Same studio room, same lighting kit and backdrop as the reference
> image. A young Indonesian woman in her early twenties, long dark hair, natural
> minimal makeup, wearing a plain black oversized t-shirt and dark jeans. She
> sits on a black cube stool, one knee pulled up, leaning slightly forward,
> looking straight into the lens with a calm half-smile. Full body in frame,
> generous negative space to her left. Soft key light from camera left, gentle
> shadow falloff across the backdrop behind her.

**Must be true:** she looks like she could be anyone who booked a slot. Relaxed,
not posed by a professional — that is the product.

## 02 — second solo portrait, visibly different backdrop

**Its job:** the Instagram highlights treat backdrop choice as a selling point.
This frame is the proof, and it only works if the backdrop obviously differs
from 01. Same room, different wall.

> [house style] Same studio room and lighting kit as the reference image, but a
> different backdrop: a deep charcoal grey seamless paper instead of white. A
> young Indonesian man in his mid-twenties, short dark hair, wearing a plain
> white t-shirt and dark trousers, standing three-quarters to camera with hands
> in pockets, chin slightly lifted, a small confident smile. Strong side light
> from camera right carving out his jaw and shoulder, dark backdrop falling to
> near black behind him. Three-quarter length, plenty of room above his head.

**Must be true:** white shirt on dark ground, after dark shirt on light ground in
01. The inversion is what makes "you can pick your backdrop" legible at a glance.

## 03 — two people

**Its job:** the most-booked shape, and what *"Untuk berdua"* on the MINI card
describes.

> [house style] Same studio room, same lighting kit as the reference image, white
> seamless backdrop. Two Indonesian women in their early twenties, best friends,
> photographed mid-laugh — one wearing a hijab and a cream long-sleeve top, the
> other with long loose hair and a striped shirt. They lean into each other
> shoulder to shoulder, one throwing a peace sign, both genuinely laughing rather
> than posing. Soft even frontal light, bright and open. Waist-up, centred, a
> little space either side.

**Must be true:** friends, not a couple. Two friends is at least as common a
booking, and it does not narrow who the frame speaks to. Keep the laugh real —
"mid-laugh, eyes crinkled, not a posed smile" is worth repeating in the prompt if
the first pass comes back stiff.

## 04 — a group

**Its job:** BIG MAXI sells up to ten people. This is the only place a picture
can corroborate a number on the price list.

> [house style] Same studio room, same lighting kit as the reference image, white
> seamless backdrop. Six Indonesian friends in their twenties, mixed men and
> women, one wearing a hijab, all in plain casual black, white and grey clothes.
> They are crowded together for a group photo — two crouching at the front, four
> standing behind, arms over shoulders, several laughing, one pointing at the
> camera. Energetic and slightly chaotic rather than neatly arranged. Wide shot
> with visible empty backdrop at both edges, showing the room comfortably holds
> more than the group in it. Broad even light across the whole group.

**Must be true:** the space at the edges. Six people filling the frame edge to
edge says *six is a squeeze*; six people with room either side says *ten would
fit*, which is the actual claim.

## 06 — the printed strip

**Already real. Keep it.** It carries the true lockup, the true 4-panel layout
and real customers. It is the single most valuable image in the set and nothing
generated will beat it.

Only if a variant is ever needed — and it should be photographed, not generated,
because the layout and lockup have to be exact:

> A hand holding a printed black-and-white photo booth strip up against a plain
> concrete wall in daylight, shot slightly from above. The strip has four panels
> of friends posing, and a black footer with a small logo. Natural window light
> raking across the wall, soft shadow behind the strip. Shallow depth of field,
> the strip sharp and the wall softly out of focus. Monochrome, realistic
> photograph, 3:2 landscape.

---

## One more frame worth having

The price list sells **Pas Foto**, **Marry Me (pas foto)** and **Pas Foto Nikah
Dinas** — formal ID photographs, including for marriage registration, at
Rp 50.000 to Rp 250.000. Three of the seven packages, and the gallery says
nothing about them. Anyone arriving from a search for *pas foto Banyuwangi* sees
six casual portraits and no evidence the studio does the thing they came for.

> [house style] Same studio room and lighting kit as the reference image, plain
> light grey backdrop. A tightly framed formal identification portrait: an
> Indonesian couple in their late twenties, the man in a dark suit jacket and the
> woman in a neat blouse and hijab, standing shoulder to shoulder, both facing
> the camera squarely with composed neutral expressions. Flat even frontal
> lighting with no shadow on the backdrop, exactly as a formal document photo
> requires. Head and shoulders, centred, symmetrical.

Adding it means seven images. The grid is two columns, so seven leaves one cell
short on the last row — either add an eighth or drop one of the two solos, which
carry the least weight.

---

## Checking a generated frame before it ships

Fastest tell that a frame is not working:

- **Hands.** Still the first thing to break, and frames 03, 04 and 06 all have
  hands doing something specific. Count fingers.
- **The lockup.** If any generated frame has invented a logo or lettering, it is
  unusable — that is a fake mark on a real brand's page. Regenerate, do not
  retouch.
- **Two rooms.** Put 01 through 05 side by side. If they do not read as one place,
  Rule 1 was not applied.
- **The edges.** The grid fills a 3:2 box with `object-fit: cover`. At the site's
  ratio nothing is cropped, but keep anything essential out of the outer 5%
  anyway, in case the layout changes.
