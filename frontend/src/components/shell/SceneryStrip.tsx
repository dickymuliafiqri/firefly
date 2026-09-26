import { useParallax } from '@/lib/parallax';
import forestSvg from '@/assets/forest.svg';

/**
 * Scenery strip — Overview & EmptyState only (DESIGN_RULES §7, Decision B).
 * Single img layer, depth:8, behind content (.page-col content z-index 1).
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
