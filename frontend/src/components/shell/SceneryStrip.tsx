import { useParallax } from '@/lib/parallax';
import forestSvg from '@/assets/forest.svg';

/**
 * Scenery strip — hanya di Overview & EmptyState (DESIGN_RULES §7, Keputusan B).
 * Satu lapis img, depth:8, di belakang konten (konten .page-col z-index 1).
 */
export function SceneryStrip() {
  const layerRef = useParallax<HTMLDivElement>(8);

  return (
    <div className="scenery" aria-hidden="true">
      <div className="layer" ref={layerRef}>
        <img src={forestSvg} alt="" />
      </div>
    </div>
  );
}
