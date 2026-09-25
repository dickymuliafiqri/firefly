import { starPositions } from '@/data/mock';
import { useParallax } from '@/lib/parallax';
import moonSvg from '@/assets/moon.svg';

/**
 * Sky global — satu instance di level shell (Keputusan B, DESIGN_RULES §7).
 * Maksimum 3 elemen parallax: bintang depth:3, bulan depth:-9.
 */
export function SkyBackground() {
  const starsRef = useParallax<HTMLDivElement>(3, 'stars');
  const moonRef = useParallax<HTMLDivElement>(-9, 'moon');

  return (
    <div className="sky-global" aria-hidden="true">
      <div className="sky-star-wrapper" ref={starsRef}>
        {starPositions.map(([left, top, d], i) => (
          <i key={i} style={{ left, top, ['--d' as string]: d }} />
        ))}
      </div>
      <div className="sky-moon" ref={moonRef}>
        <img src={moonSvg} alt="" width={120} height={120} />
      </div>
      <div className="sky-horizonlight" />
    </div>
  );
}
