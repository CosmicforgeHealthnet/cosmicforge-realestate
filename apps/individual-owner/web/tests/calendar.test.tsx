import {afterEach,describe,expect,it,vi} from 'vitest';
import {cleanup,fireEvent,render,screen,within} from '@testing-library/react';
import {Calendar,calendarEventOnDay,calendarMonthLayout} from '@/components/workspace-ui';

afterEach(()=>cleanup());

describe('owner calendar layout and navigation',()=>{
  it('builds true Monday-start months with whole weeks',()=>{
    expect(calendarMonthLayout('2026-10')).toMatchObject({days:31,offset:3,first:'2026-10-01',next:'2026-11-01'});
    expect(calendarMonthLayout('2026-11')).toMatchObject({days:30,offset:6});
    expect(calendarMonthLayout('2027-02')).toMatchObject({days:28,offset:0});
  });

  it('renders all seven weekdays and compact month without missing Friday to Sunday',()=>{
    const view=render(<Calendar events={[]} onSelect={vi.fn()}/>);
    expect(view.container.querySelectorAll('.calendar-weekdays span')).toHaveLength(7);
    expect([...view.container.querySelectorAll('.calendar-weekdays span')].map(x=>x.textContent))
      .toEqual(['Mon','Tue','Wed','Thu','Fri','Sat','Sun']);
    expect(view.container.querySelectorAll('.calendar-day')).toHaveLength(31);
    expect(view.container.querySelectorAll('.calendar-blank')).toHaveLength(1);
    expect(screen.getByRole('heading',{name:'October 2026'})).toBeInTheDocument();
    expect(screen.getByRole('button',{name:'Show events on 2026-10-31'})).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button',{name:'Next month'}));
    expect(screen.getByRole('heading',{name:'November 2026'})).toBeInTheDocument();
    expect(view.container.querySelectorAll('.calendar-day')).toHaveLength(30);
    expect(view.container.querySelectorAll('.calendar-blank')).toHaveLength(12);
    fireEvent.click(screen.getByRole('button',{name:'Previous month'}));
    expect(screen.getByRole('heading',{name:'October 2026'})).toBeInTheDocument();
  });

  it('lets owners select a date, open its events and return to the month agenda',()=>{
    const onSelect=vi.fn();
    render(<Calendar onSelect={onSelect} events={[
      {id:'v-1',title:'Tenant viewing',date:'2026-10-15',status:'APPROVED'},
      {id:'m-1',title:'Maintenance inspection',date:'2026-10-15',status:'PENDING'},
      {id:'b-1',title:'Short-let stay',date:'2026-10-14',end:'2026-10-18',status:'CONFIRMED'},
    ]}/>);
    fireEvent.click(screen.getByRole('button',{name:'Show events on 2026-10-15'}));
    expect(screen.getByRole('heading',{name:'Events · 2026-10-15'})).toBeInTheDocument();
    const agenda=screen.getByRole('region',{name:'Calendar event agenda'});
    expect(within(agenda).getAllByRole('button')).toHaveLength(3);
    fireEvent.click(within(agenda).getByRole('button',{name:/Tenant viewing/i}));
    expect(onSelect).toHaveBeenCalledWith('v-1');
    fireEvent.click(screen.getByRole('button',{name:'Show whole month'}));
    expect(screen.getByRole('heading',{name:'This month’s activity'})).toBeInTheDocument();
  });

  it('treats multi-day booking end as exclusive',()=>{
    const booking={id:'stay',title:'Stay',date:'2026-10-14',end:'2026-10-18',status:'CONFIRMED'};
    expect(calendarEventOnDay(booking,'2026-10-17')).toBe(true);
    expect(calendarEventOnDay(booking,'2026-10-18')).toBe(false);
  });
});
